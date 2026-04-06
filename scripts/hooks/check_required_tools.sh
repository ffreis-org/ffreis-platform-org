#!/usr/bin/env bash
set -euo pipefail
IFS=$'\n\t'

if [[ "$#" -eq 0 ]]; then
  echo "Usage: $0 <tool> [tool...]" >&2
  exit 1
fi

missing=0

install_hint() {
  local tool_name="$1"

  case "$tool_name" in
    terraform)
      echo "https://developer.hashicorp.com/terraform/downloads"
      ;;
    go|gofmt|govulncheck)
      echo "https://go.dev/dl/"
      ;;
    tflint)
      echo "https://github.com/terraform-linters/tflint#installation"
      ;;
    gitleaks)
      echo "https://github.com/gitleaks/gitleaks#installing"
      ;;
    trivy)
      echo "https://trivy.dev/latest/getting-started/installation/"
      ;;
    *)
      echo "Install '${tool_name}' and ensure it is available in PATH."
      ;;
  esac

  return 0
}

for tool in "$@"; do
  if command -v "$tool" >/dev/null 2>&1; then
    continue
  fi
  echo "Missing required tool: $tool" >&2
  echo "Install hint: $(install_hint "$tool")" >&2
  missing=1
done

exit "$missing"
