#!/usr/bin/env ruby
# frozen_string_literal: true

require "yaml"

workflow_path = File.expand_path("../workflows/test.yml", __dir__)
workflow = YAML.safe_load(File.read(workflow_path), permitted_classes: [], aliases: false)
test_job = workflow.fetch("jobs").fetch("test")
steps = test_job.fetch("steps")

failures = []
expect = lambda do |condition, message|
  failures << message unless condition
end

packages = test_job.fetch("env").fetch("LINUX_DESKTOP_PACKAGES").split
expect.call(
  packages == [
    "pkg-config",
    "libgtk-4-dev",
    "libwebkitgtk-6.0-dev",
    "libglib2.0-dev",
    "libsoup-3.0-dev"
  ],
  "Linux desktop package manifest changed"
)

uses = steps.map { |step| step["uses"] }.compact
expect.call(
  uses.all? { |reference| reference.match?(%r{\A[^@]+@[0-9a-f]{40}(?:\s+#.*)?\z}) },
  "all workflow actions must use full commit SHAs"
)

archive_cache = steps.find { |step| step["id"] == "linux-desktop-cache" }
expect.call(!archive_cache.nil?, "archive cache step is missing")
if archive_cache
  expect.call(
    archive_cache.dig("with", "path") == "~/.cache/openvibely-apt/archives/*.deb",
    "archive cache path changed"
  )
  expect.call(
    archive_cache.dig("with", "key") == "${{ steps.linux-desktop-cache-key.outputs.key }}",
    "archive cache key wiring changed"
  )
end

key_step = steps.find { |step| step["id"] == "linux-desktop-cache-key" }
key_script = key_step&.fetch("run", "")
expect.call(key_script.include?("ImageOS") && key_script.include?("ImageVersion") && key_script.include?("RUNNER_ARCH"), "cache key must include runner identity and architecture")
expect.call(key_script.include?("LINUX_DESKTOP_PACKAGES") && key_script.include?("sha256sum"), "cache key must include the package manifest digest")
expect.call(key_script.include?("linux-desktop-apt-v1"), "archive cache namespace changed")
expect.call(key_script.include?("linux-desktop-apt-lists-v1") && key_script.include?("metadata_date"), "APT list cache must have an explicit freshness window")

metadata_cache = steps.find { |step| step["id"] == "linux-desktop-metadata-cache" }
expect.call(!metadata_cache.nil?, "APT list cache step is missing")
if metadata_cache
  expect.call(metadata_cache.dig("with", "path") == "~/.cache/openvibely-apt/lists", "APT list cache path changed")
  expect.call(metadata_cache.dig("with", "key") == "${{ steps.linux-desktop-cache-key.outputs.metadata_key }}", "APT list cache key wiring changed")
end

install_step = steps.find { |step| step["name"] == "Install Linux desktop dependencies" }
install_script = install_step&.fetch("run", "")
expect.call(install_script.include?("-s install") && install_script.include?("metadata_fresh=true"), "cached metadata must pass a non-mutating APT preflight")
expect.call(install_script.include?("apt_update_count=0") && install_script.include?("Dir::State::lists=\"$lists_directory\" update"), "cold, stale, or invalid metadata must update APT")
expect.call(install_script.include?("cached-install-failed+updated") && install_script.scan("Dir::State::lists=\"$lists_directory\" update").length >= 2, "failed cached installation must retry after an APT update")
expect.call(install_script.include?("apt_install --no-download") && install_script.include?("archive_cache_hit\" == \"true\" && \"$metadata_fresh\" == \"true"), "valid archive and metadata hits must use the no-download fast path")
expect.call(install_script.include?("Dir::Cache::archives") && install_script.include?("APT::Keep-Downloaded-Packages=true"), "archive-backed installation options changed")
expect.call(install_script.include?("linux-desktop-dependency.json") && install_script.include?("archive_downloaded_bytes"), "dependency timing evidence is missing archive download metrics")

job_timing_step = steps.find { |step| step["name"] == "Record Linux test job timing" }
expect.call(!job_timing_step.nil? && job_timing_step.fetch("run", "").include?("linux-test-job.json"), "complete-job timing evidence is missing")

artifact_step = steps.find { |step| step["name"] == "Upload Linux desktop dependency timing" }
expect.call(!artifact_step.nil?, "timing artifact upload is missing")
if artifact_step
  expect.call(artifact_step["if"] == "always()", "timing artifact must upload after failures")
  expect.call(artifact_step.dig("with", "path") == "ci-timing", "timing artifact path changed")
end

abort("workflow invariant failures:\n- #{failures.join("\n- ")}") unless failures.empty?
puts "workflow invariants passed"
