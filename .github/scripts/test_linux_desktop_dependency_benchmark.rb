#!/usr/bin/env ruby
# frozen_string_literal: true

require "json"
require "open3"
require "rbconfig"
require "tmpdir"

script = File.expand_path("check_linux_desktop_dependency_benchmark.rb", __dir__)

def write_sample(root, variant:, scenario:, sample_id:, dependency_ms:, job_ms:, metadata_validation:, apt_updates:)
  directory = File.join(root, "#{variant}-#{scenario}-#{sample_id}")
  Dir.mkdir(directory)
  common = {
    "schema_version" => 2,
    "benchmark_variant" => variant,
    "benchmark_scenario" => scenario,
    "benchmark_sample_id" => sample_id,
    "github_run_id" => "benchmark-run",
    "runner_image" => "ubuntu-24.04-x64"
  }
  dependency = common.merge(
    "dependency_setup_duration_ms" => dependency_ms,
    "archive_cache_restore_outcome" => "success",
    "metadata_cache_restore_outcome" => "success",
    "archive_cache_hit" => "true",
    "metadata_cache_hit" => scenario == "cache-hit" ? "true" : "false",
    "metadata_validation" => metadata_validation,
    "apt_update_count" => apt_updates,
    "apt_install_attempts" => 1,
    "install_succeeded" => true,
    "archive_downloaded_bytes" => 0
  )
  job = common.merge("job_timing_duration_ms" => job_ms)
  File.write(File.join(directory, "linux-desktop-dependency.json"), JSON.generate(dependency))
  File.write(File.join(directory, "linux-test-job.json"), JSON.generate(job))
end

def assert(condition, message)
  raise message unless condition
end

Dir.mktmpdir("linux-desktop-benchmark") do |directory|
  (1..10).each do |sample|
    write_sample(directory, variant: "baseline", scenario: "cache-hit", sample_id: sample, dependency_ms: 1_000 + sample, job_ms: 10_000 + sample, metadata_validation: "fresh-preflight+benchmark-baseline-updated", apt_updates: 1)
    write_sample(directory, variant: "candidate", scenario: "cache-hit", sample_id: sample, dependency_ms: 700 + sample, job_ms: 9_800 + sample, metadata_validation: "fresh-preflight", apt_updates: 0)
  end
  %w[cold partial cold].each_with_index do |scenario, index|
    write_sample(directory, variant: "candidate", scenario: scenario, sample_id: "recovery-#{index}", dependency_ms: 1_500, job_ms: 10_100, metadata_validation: "cache-miss+updated", apt_updates: 1)
  end

  output = File.join(directory, "summary", "linux-desktop-dependency-benchmark.json")
  stdout, stderr, status = Open3.capture3(RbConfig.ruby, script, "--artifacts-dir", directory, "--output", output)
  assert(status.success?, "expected valid benchmark evidence to pass: #{stdout}\n#{stderr}")
  summary = JSON.parse(File.read(output))
  assert(summary.dig("dependency_setup_improvement", "p50") >= 0.20, "summary did not retain p50 improvement")
  assert(summary.dig("dependency_setup_improvement", "p95") >= 0.20, "summary did not retain p95 improvement")

  duplicate_recovery = JSON.parse(File.read(File.join(directory, "candidate-cold-recovery-0", "linux-desktop-dependency.json")))
  duplicate_recovery["benchmark_sample_id"] = "recovery-1"
  File.write(File.join(directory, "candidate-cold-recovery-0", "linux-desktop-dependency.json"), JSON.generate(duplicate_recovery))
  _stdout, stderr, status = Open3.capture3(RbConfig.ruby, script, "--artifacts-dir", directory, "--output", output)
  assert(!status.success?, "expected duplicate recovery samples to fail")
  assert(stderr.include?("3 distinct sample IDs"), "expected duplicate recovery failure, got: #{stderr}")
  duplicate_recovery["benchmark_sample_id"] = "recovery-0"
  File.write(File.join(directory, "candidate-cold-recovery-0", "linux-desktop-dependency.json"), JSON.generate(duplicate_recovery))

  candidate_file = File.join(directory, "candidate-cache-hit-10", "linux-desktop-dependency.json")
  candidate = JSON.parse(File.read(candidate_file))
  candidate["dependency_setup_duration_ms"] = 1_500
  File.write(candidate_file, JSON.generate(candidate))
  _stdout, stderr, status = Open3.capture3(RbConfig.ruby, script, "--artifacts-dir", directory, "--output", output)
  assert(!status.success?, "expected benchmark below the p95 threshold to fail")
  assert(stderr.include?("benchmark acceptance failed"), "expected threshold failure, got: #{stderr}")
end

puts "Linux desktop benchmark comparator tests passed"
