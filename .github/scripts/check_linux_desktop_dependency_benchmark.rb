#!/usr/bin/env ruby
# frozen_string_literal: true

require "json"
require "optparse"
require "fileutils"

options = {}
OptionParser.new do |parser|
  parser.banner = "Usage: #{File.basename($PROGRAM_NAME)} --artifacts-dir DIR --output FILE"
  parser.on("--artifacts-dir DIR", "Directory containing downloaded timing artifacts") { |value| options[:artifacts_dir] = value }
  parser.on("--output FILE", "Write the accepted benchmark summary JSON here") { |value| options[:output] = value }
end.parse!

abort("--artifacts-dir is required") unless options[:artifacts_dir]
abort("--output is required") unless options[:output]

errors = []
def require_field(sample, field, errors)
  return sample.fetch(field) if sample.key?(field)

  errors << "#{sample.fetch('_path')} is missing #{field}"
  nil
end

def percentile(values, percentile)
  values.sort.fetch((values.length * percentile).ceil - 1)
end

def statistics(samples, dependency_field, job_field)
  dependency_durations = samples.map { |sample| sample.fetch(dependency_field) }.sort
  job_durations = samples.map { |sample| sample.fetch(job_field) }.sort
  {
    "sample_count" => samples.length,
    "dependency_setup_duration_ms" => {
      "p50" => percentile(dependency_durations, 0.50),
      "p95" => percentile(dependency_durations, 0.95)
    },
    "complete_job_duration_ms" => {
      "p50" => percentile(job_durations, 0.50),
      "p95" => percentile(job_durations, 0.95)
    }
  }
end

def improvement(baseline, candidate)
  (baseline - candidate).fdiv(baseline)
end

dependency_paths = Dir.glob(File.join(options[:artifacts_dir], "**", "linux-desktop-dependency.json")).sort
errors << "no dependency timing artifacts found under #{options[:artifacts_dir]}" if dependency_paths.empty?
samples = dependency_paths.map do |dependency_path|
  job_path = File.join(File.dirname(dependency_path), "linux-test-job.json")
  unless File.file?(job_path)
    errors << "#{dependency_path} has no sibling linux-test-job.json"
    next
  end

  begin
    dependency = JSON.parse(File.read(dependency_path))
    job = JSON.parse(File.read(job_path))
  rescue JSON::ParserError => e
    errors << "invalid timing JSON beside #{dependency_path}: #{e.message}"
    next
  end

  dependency["_path"] = dependency_path
  %w[benchmark_variant benchmark_scenario benchmark_sample_id github_run_id].each do |field|
    dependency_value = require_field(dependency, field, errors)
    job_value = require_field(job, field, errors)
    errors << "#{dependency_path} and #{job_path} disagree on #{field}" if dependency_value && job_value && dependency_value != job_value
  end
  %w[runner_image dependency_setup_duration_ms archive_cache_hit metadata_cache_hit metadata_validation archive_cache_restore_outcome metadata_cache_restore_outcome apt_update_count apt_install_attempts install_succeeded archive_downloaded_bytes].each do |field|
    require_field(dependency, field, errors)
  end
  require_field(job, "job_timing_duration_ms", errors)
  dependency.merge("job_timing_duration_ms" => job["job_timing_duration_ms"])
end.compact
baseline_hits = samples.select { |sample| sample["benchmark_variant"] == "baseline" && sample["benchmark_scenario"] == "cache-hit" }
candidate_hits = samples.select { |sample| sample["benchmark_variant"] == "candidate" && sample["benchmark_scenario"] == "cache-hit" }
recovery_samples = samples.select do |sample|
  sample["benchmark_variant"] == "candidate" && %w[cold partial].include?(sample["benchmark_scenario"])
end

[["baseline", baseline_hits], ["candidate", candidate_hits]].each do |label, hit_samples|
  errors << "need at least 10 valid #{label} cache-hit samples; found #{hit_samples.length}" if hit_samples.length < 10
  sample_ids = hit_samples.map { |sample| sample["benchmark_sample_id"] }.uniq
  errors << "#{label} cache-hit evidence must contain 10 distinct sample IDs; found #{sample_ids.length}" if sample_ids.length < 10
  hit_samples.each do |sample|
    unless sample["archive_cache_restore_outcome"] == "success" && sample["metadata_cache_restore_outcome"] == "success" && sample["archive_cache_hit"] == "true"
      errors << "#{sample.fetch('_path')} is not a successful archive cache hit"
    end
    unless sample["install_succeeded"] == true && sample["archive_downloaded_bytes"].to_i.zero?
      errors << "#{sample.fetch('_path')} did not complete without archive downloads"
    end
  end
end

candidate_hits.each do |sample|
  unless sample["metadata_cache_hit"] == "true" && sample["apt_update_count"].to_i.zero? && sample["metadata_validation"] == "fresh-preflight"
    errors << "#{sample.fetch('_path')} is not a valid candidate metadata fast path"
  end
end

baseline_hits.each do |sample|
  errors << "#{sample.fetch('_path')} did not run the baseline metadata refresh" unless sample["apt_update_count"].to_i.positive?
end

errors << "need at least 3 successful candidate cold/partial recovery samples; found #{recovery_samples.length}" if recovery_samples.length < 3
recovery_samples.each do |sample|
  unless sample["install_succeeded"] == true && sample["apt_update_count"].to_i.positive? && sample["metadata_validation"].include?("updated")
    errors << "#{sample.fetch('_path')} did not use the safe update/install recovery path"
  end
end

benchmark_images = (baseline_hits + candidate_hits + recovery_samples).map { |sample| sample["runner_image"] }.uniq
errors << "benchmark samples used non-equivalent runner images: #{benchmark_images.join(', ')}" unless benchmark_images.length == 1

abort("benchmark evidence rejected:\n- #{errors.join("\n- ")}") unless errors.empty?

baseline = statistics(baseline_hits, "dependency_setup_duration_ms", "job_timing_duration_ms")
candidate = statistics(candidate_hits, "dependency_setup_duration_ms", "job_timing_duration_ms")
dependency_p50_improvement = improvement(baseline.dig("dependency_setup_duration_ms", "p50"), candidate.dig("dependency_setup_duration_ms", "p50"))
dependency_p95_improvement = improvement(baseline.dig("dependency_setup_duration_ms", "p95"), candidate.dig("dependency_setup_duration_ms", "p95"))
job_p50_regressed = candidate.dig("complete_job_duration_ms", "p50") > baseline.dig("complete_job_duration_ms", "p50")
job_p95_regressed = candidate.dig("complete_job_duration_ms", "p95") > baseline.dig("complete_job_duration_ms", "p95")

if dependency_p50_improvement < 0.20 || dependency_p95_improvement < 0.20 || job_p50_regressed || job_p95_regressed
  abort(format(
    "benchmark acceptance failed: dependency p50 improvement %.2f%%, p95 improvement %.2f%%; job p50 regression=%s, p95 regression=%s",
    dependency_p50_improvement * 100,
    dependency_p95_improvement * 100,
    job_p50_regressed,
    job_p95_regressed
  ))
end

summary = {
  "schema_version" => 1,
  "runner_image" => benchmark_images.fetch(0),
  "acceptance" => {
    "minimum_cache_hit_samples_per_variant" => 10,
    "minimum_cold_or_partial_samples" => 3,
    "minimum_dependency_setup_improvement" => 0.20,
    "complete_job_regression" => false
  },
  "baseline" => baseline,
  "candidate" => candidate,
  "recovery_sample_count" => recovery_samples.length,
  "dependency_setup_improvement" => {
    "p50" => dependency_p50_improvement,
    "p95" => dependency_p95_improvement
  }
}
FileUtils.mkdir_p(File.dirname(options[:output]))
File.write(options[:output], "#{JSON.pretty_generate(summary)}\n")
puts JSON.generate(summary)
