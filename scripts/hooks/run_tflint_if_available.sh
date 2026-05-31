#!/usr/bin/env bash
set -euo pipefail
IFS=$'\n\t'

if ! command -v tflint >/dev/null 2>&1; then
	echo "Skipping tflint in git hook: tflint is not installed locally." >&2
	echo "Install hint: https://github.com/terraform-linters/tflint#installation" >&2
	exit 0
fi

# tflint v0.47+ dropped --recursive in favour of --chdir / --filter.
# Iterate every leaf module directory under terraform/ that has any .tf files.
tflint --init >/dev/null

# Some installs (notably snap) cannot read paths outside their confinement.
# A quick probe before iterating prevents a confusing "permission denied"
# failure for every module.
probe_dir="$(find terraform -type f -name '*.tf' -not -path '*/.terraform/*' -exec dirname {} \; | sort -u | head -1)"
if [[ -n "$probe_dir" ]]; then
	probe_out=$(tflint --chdir="$probe_dir" --format compact 2>&1) || probe_rc=$?
	if [[ "${probe_rc:-0}" -ne 0 && "$probe_out" == *"permission denied"* ]]; then
		echo "Skipping tflint in git hook: tflint cannot read the project tree." >&2
		echo "Likely cause: snap-confined tflint and a project on a non-standard mount." >&2
		echo "Install tflint via apt or the official tarball to enable local linting." >&2
		exit 0
	fi
fi

rc=0
while IFS= read -r tfdir; do
	if ! tflint --chdir="$tfdir" --format compact; then
		rc=1
	fi
done < <(find terraform -type f -name '*.tf' -not -path '*/.terraform/*' -exec dirname {} \; | sort -u)
exit "$rc"
