#!/usr/bin/env bash
# ---------------------------------------------------------------------------
# validate.sh — post-apply validation for platform-org
#
# Checks:
#   1. AWS Organization exists
#   2. Member accounts exist (development, staging, production)
#   3. SCPs are attached to the environments OU
#   4. Runtime backend (S3 + DynamoDB) exists
#   5. Terraform state is readable
#
# Usage:
#   ./scripts/validate.sh [--profile <profile>] [--env <env>]
#
# Defaults: profile=bootstrap, env=prod
# ---------------------------------------------------------------------------
set -euo pipefail

PROFILE="${PROFILE:-bootstrap}"
ENV="${ENV:-prod}"
STACK_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../terraform/stack" && pwd)"
ENVS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../terraform/envs" && pwd)"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --profile) PROFILE="$2"; shift 2 ;;
    --env)     ENV="$2";     shift 2 ;;
    *) echo "Unknown argument: $1" >&2; exit 1 ;;
  esac
done

AWS=(aws --profile "${PROFILE}" --output json)
PASS="[ PASS ]"
FAIL="[ FAIL ]"
errors=0

fail() { echo "${FAIL} $*" >&2; errors=$((errors + 1)); return 0; }
pass() { echo "${PASS} $*"; return 0; }

require_tool() {
  local tool_name="$1"

  command -v "${tool_name}" >/dev/null 2>&1 || {
    echo "Required tool not found: ${tool_name}" >&2
    exit 1
  }

  return 0
}

require_tool aws
require_tool jq
require_tool terraform

echo ""
echo "=== platform-org validation (env=${ENV}, profile=${PROFILE}) ==="
echo ""

# ---------------------------------------------------------------------------
# 1. Organization exists
# ---------------------------------------------------------------------------
echo "--- 1. Organization ---"

org_json="$(${AWS[@]} organizations describe-organization 2>/dev/null || true)"
if [[ -z "${org_json}" ]]; then
  fail "Organization not found or no access"
else
  org_id="$(echo "${org_json}" | jq -r '.Organization.Id')"
  feature_set="$(echo "${org_json}" | jq -r '.Organization.FeatureSet')"
  pass "Organization found: ${org_id}"

  if [[ "${feature_set}" == "ALL" ]]; then
    pass "FeatureSet=ALL (SCPs enabled)"
  else
    fail "FeatureSet=${feature_set} (expected ALL)"
  fi

  scp_enabled="$(${AWS[@]} organizations list-roots 2>/dev/null | jq -r '
    .Roots[0].PolicyTypes[]? | select(.Type == "SERVICE_CONTROL_POLICY") | .Status' || true)"
  if [[ "${scp_enabled}" == "ENABLED" ]]; then
    pass "SERVICE_CONTROL_POLICY type is ENABLED on root"
  else
    fail "SERVICE_CONTROL_POLICY type is not enabled on root (status=${scp_enabled:-not found})"
  fi
fi

echo ""

# ---------------------------------------------------------------------------
# 2. Member accounts exist
# ---------------------------------------------------------------------------
echo "--- 2. Member accounts ---"

expected_accounts=()
fetched_file="${ENVS_DIR}/${ENV}/fetched.auto.tfvars.json"
if [[ -f "${fetched_file}" ]]; then
  mapfile -t expected_accounts < <(jq -r '.accounts | keys[]' "${fetched_file}" 2>/dev/null || true)
fi
if [[ ${#expected_accounts[@]} -eq 0 ]]; then
  expected_accounts=("development" "staging" "production")
fi

accounts_json="$(${AWS[@]} organizations list-accounts 2>/dev/null || true)"
if [[ -z "${accounts_json}" ]]; then
  fail "Could not list accounts"
else
  for name in "${expected_accounts[@]}"; do
    account_id="$(echo "${accounts_json}" | jq -r \
      --arg name "${name}" '.Accounts[] | select(.Name == $name) | .Id')"
    if [[ -n "${account_id}" ]]; then
      status="$(echo "${accounts_json}" | jq -r \
        --arg name "${name}" '.Accounts[] | select(.Name == $name) | .Status')"
      pass "Account '${name}': ${account_id} (${status})"
    else
      fail "Account '${name}' not found"
    fi
  done
fi

echo ""

# ---------------------------------------------------------------------------
# 3. SCPs attached to environments OU
# ---------------------------------------------------------------------------
echo "--- 3. SCPs on environments OU ---"

expected_scps=(
  "deny-iam-user-creation"
  "deny-disable-cloudtrail"
  "deny-leave-organization"
)

# Resolve environments OU ID
env_ou_id=""
if [[ -n "${org_id:-}" ]]; then
  root_id="$(${AWS[@]} organizations list-roots 2>/dev/null | jq -r '.Roots[0].Id' || true)"
  if [[ -n "${root_id}" ]]; then
    env_ou_id="$(${AWS[@]} organizations list-organizational-units-for-parent \
      --parent-id "${root_id}" 2>/dev/null | \
      jq -r '.OrganizationalUnits[] | select(.Name == "environments") | .Id' || true)"
  fi
fi

if [[ -z "${env_ou_id}" ]]; then
  fail "Could not resolve environments OU ID"
else
  pass "Environments OU found: ${env_ou_id}"

  attached_scps="$(${AWS[@]} organizations list-policies-for-target \
    --target-id "${env_ou_id}" \
    --filter SERVICE_CONTROL_POLICY 2>/dev/null | jq -r '.Policies[].Name' || true)"

  for scp_name in "${expected_scps[@]}"; do
    if echo "${attached_scps}" | grep -qx "${scp_name}"; then
      pass "SCP attached: ${scp_name}"
    else
      fail "SCP not attached: ${scp_name}"
    fi
  done
fi

echo ""

# ---------------------------------------------------------------------------
# 4. Runtime backend exists
# ---------------------------------------------------------------------------
echo "--- 4. Runtime backend ---"

# Read org from fetched config (fetched.auto.tfvars.json is required for the
# runtime backend check — the committed terraform.tfvars does not contain org).
fetched_json="${ENVS_DIR}/${ENV}/fetched.auto.tfvars.json"
if [[ ! -f "${fetched_json}" ]]; then
  fail "fetched.auto.tfvars.json not found at ${fetched_json} — run 'make fetch ENV=${ENV}' first"
  exit 1
fi
org="$(jq -r '.org // empty' "${fetched_json}" 2>/dev/null || true)"
if [[ -z "${org}" ]]; then
  fail "Could not determine 'org' from ${fetched_json} — ensure it contains an 'org' field"
  exit 1
fi
runtime_bucket="${org}-tf-state-runtime"
runtime_table="${org}-tf-locks-runtime"

if ${AWS[@]} s3api head-bucket --bucket "${runtime_bucket}" 2>/dev/null; then
  pass "S3 bucket exists: ${runtime_bucket}"

  versioning="$(${AWS[@]} s3api get-bucket-versioning --bucket "${runtime_bucket}" | \
    jq -r '.Status // "Disabled"')"
  if [[ "${versioning}" == "Enabled" ]]; then
    pass "Versioning enabled on ${runtime_bucket}"
  else
    fail "Versioning not enabled on ${runtime_bucket} (status=${versioning})"
  fi

  encryption="$(${AWS[@]} s3api get-bucket-encryption --bucket "${runtime_bucket}" 2>/dev/null | \
    jq -r '.ServerSideEncryptionConfiguration.Rules[0].ApplyServerSideEncryptionByDefault.SSEAlgorithm // ""')"
  if [[ -n "${encryption}" ]]; then
    pass "Encryption enabled on ${runtime_bucket}: ${encryption}"
  else
    fail "Encryption not configured on ${runtime_bucket}"
  fi
else
  fail "S3 bucket not found: ${runtime_bucket}"
fi

table_status="$(${AWS[@]} dynamodb describe-table \
  --table-name "${runtime_table}" 2>/dev/null | \
  jq -r '.Table.TableStatus // ""')"
if [[ "${table_status}" == "ACTIVE" ]]; then
  pass "DynamoDB table active: ${runtime_table}"
else
  fail "DynamoDB table not found or not active: ${runtime_table} (status=${table_status:-not found})"
fi

echo ""

# ---------------------------------------------------------------------------
# 5. Terraform state is readable
# ---------------------------------------------------------------------------
echo "--- 5. Terraform state ---"

state_key="platform-org/${ENV}/terraform.tfstate"
backend_hcl="${STACK_DIR}/backend.local.hcl"
if [[ ! -f "${backend_hcl}" ]]; then
  fail "backend.local.hcl not found at ${backend_hcl} — run 'make fetch ENV=${ENV}' first"
  exit 1
fi
root_bucket="$(awk -F'=' '/^[[:space:]]*bucket[[:space:]]*=/ { gsub(/[[:space:]"]+/, "", $2); print $2 }' "${backend_hcl}")"
if [[ -z "${root_bucket}" ]]; then
  fail "Could not parse 'bucket' from ${backend_hcl}"
  exit 1
fi

if ${AWS[@]} s3api head-object \
    --bucket "${root_bucket}" \
    --key "${state_key}" >/dev/null 2>&1; then
  pass "State file exists: s3://${root_bucket}/${state_key}"

  # Verify state contains expected resources
  state_content="$(${AWS[@]} s3 cp \
    "s3://${root_bucket}/${state_key}" - 2>/dev/null || true)"
  if [[ -n "${state_content}" ]]; then
    resource_count="$(echo "${state_content}" | \
      jq '.resources | length' 2>/dev/null || echo 0)"
    pass "State readable, ${resource_count} resources tracked"
  else
    fail "State file exists but could not be read"
  fi
else
  fail "State file not found: s3://${root_bucket}/${state_key}"
  echo "       Has 'terraform apply' been run for env=${ENV}?" >&2
fi

# Also run terraform validate for static correctness
echo ""
echo "--- terraform validate ---"
pushd "${STACK_DIR}" >/dev/null
if [[ ! -d ".terraform" ]]; then
  terraform init -backend=false -input=false -no-color >/dev/null 2>&1 || true
fi
if terraform validate -no-color 2>&1; then
  pass "terraform validate passed"
else
  fail "terraform validate failed"
fi
popd >/dev/null

# ---------------------------------------------------------------------------
echo ""
if [[ "${errors}" -eq 0 ]]; then
  echo "=== All checks passed ==="
  exit 0
else
  echo "=== ${errors} check(s) failed ===" >&2
  exit 1
fi
