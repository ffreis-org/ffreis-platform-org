#!/usr/bin/env bash
# validate_terraform.sh — runs terraform validate without touching AWS.
# Injects a provider override that disables credential validation so this works
# offline and without prod credentials. Requires providers to be cached (run
# terraform init with valid credentials once after cloning or updating providers).
set -euo pipefail

STACK="terraform/stack"
OVERRIDE="${STACK}/validate_credentials_override.tf"

cleanup() {
  rm -f "${OVERRIDE}"
}
trap cleanup EXIT

cat > "${OVERRIDE}" <<'EOF'
# Temporary override injected by scripts/hooks/validate_terraform.sh.
# Suppresses credential validation so `terraform validate` works without AWS access.
# This file is created and deleted automatically — do not commit it.
provider "aws" {
  skip_credentials_validation = true
  skip_requesting_account_id  = true
  skip_metadata_api_check     = true
}
EOF

# Env vars take precedence over profile — prevents any real AWS calls during init/validate.
export AWS_ACCESS_KEY_ID=placeholder
export AWS_SECRET_ACCESS_KEY=placeholder
export AWS_DEFAULT_REGION=us-east-1
unset AWS_PROFILE

# Skip init when providers are already cached — init requires AWS credentials (provider v5
# calls STS regardless of backend config). After the first `terraform init` the cache is
# stable and re-init is only needed when provider versions change.
if [[ ! -d "${STACK}/.terraform/providers" ]]; then
  echo "providers not cached — run 'terraform -chdir=${STACK} init' with valid AWS credentials first" >&2
  exit 1
fi
terraform -chdir="${STACK}" validate -no-color
