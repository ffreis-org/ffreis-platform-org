#!/usr/bin/env bash
set -euo pipefail
IFS=$'\n\t'

source "$(dirname "$0")/../lib/common.sh"

has_error=0

common_require_git_repo

is_allowlisted() {
  local path="$1"
  common_is_allowlisted_path "$path"
  return $?
}

while IFS=$'\t' read -r added deleted file; do
  if [[ "$added" != "-" || "$deleted" != "-" ]]; then
    continue
  fi

  # Resolve rename/copy path notation to the destination path.
  # git --numstat can emit "{prefix/old => new}/suffix" or "old => new" for copies.
  actual_file="$file"
  if [[ "$file" == *" => "* ]]; then
    if [[ "$file" =~ \{([^}]*)[[:space:]]=\>[[:space:]]([^}]*)\}(.*) ]]; then
      prefix="${file%%\{*}"
      actual_file="${prefix}${BASH_REMATCH[2]}${BASH_REMATCH[3]}"
    else
      actual_file="${file##* => }"
    fi
  fi

  if is_allowlisted "$actual_file"; then
    continue
  fi

  common_err "Unexpected staged binary file: ${actual_file}"
  has_error=1
done < <(git diff --cached --numstat --diff-filter=ACM)

if [[ "$has_error" -ne 0 ]]; then
  common_err "Binary files are blocked unless they match allowlisted paths/extensions."
  exit 1
fi
