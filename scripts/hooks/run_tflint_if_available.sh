#!/usr/bin/env bash
set -euo pipefail
IFS=$'\n\t'

if ! command -v tflint >/dev/null 2>&1; then
  echo "Skipping tflint in git hook: tflint is not installed locally." >&2
  echo "Install hint: https://github.com/terraform-linters/tflint#installation" >&2
  exit 0
fi

tflint --init
tflint --recursive --format compact terraform
