#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: $0 <input-coverage-profile> <output-coverage-profile>" >&2
  exit 2
fi

input=$1
output=$2

coverage_exclusion_pattern='(_templ\.go:|^github.com/openvibely/openvibely/cmd/|^github.com/openvibely/openvibely/docs/|^github.com/openvibely/openvibely/internal/database/migrations/|^github.com/openvibely/openvibely/internal/update/testfixture/|^github.com/openvibely/openvibely/internal/service/workflow_service\.go:)'

grep -Ev "${coverage_exclusion_pattern}" "${input}" > "${output}"
