output "org_id" {
  description = "AWS Organization ID."
  value       = aws_organizations_organization.this.id
}

output "org_arn" {
  description = "AWS Organization ARN."
  value       = aws_organizations_organization.this.arn
}

output "root_id" {
  description = "Organization root ID (used as parent for top-level OUs)."
  value       = aws_organizations_organization.this.roots[0].id
}

output "environments_ou_id" {
  description = "ID of the 'environments' OU."
  value       = aws_organizations_organizational_unit.environments.id
}

output "environments_ou_arn" {
  description = "ARN of the 'environments' OU."
  value       = aws_organizations_organizational_unit.environments.arn
}

output "account_ids" {
  description = "Map of account name to AWS account ID."
  value       = { for k, v in aws_organizations_account.environments : k => v.id }
}

output "account_arns" {
  description = "Map of account name to AWS account ARN."
  value       = { for k, v in aws_organizations_account.environments : k => v.arn }
}

output "tf_state_runtime_bucket" {
  description = "Name of the S3 bucket used for runtime Terraform state."
  value       = module.tf_state_runtime.id
}

output "tf_state_runtime_bucket_arn" {
  description = "ARN of the S3 bucket used for runtime Terraform state."
  value       = module.tf_state_runtime.arn
}

output "tf_locks_runtime_table" {
  description = "Name of the DynamoDB table used for runtime Terraform state locking."
  value       = module.tf_locks_runtime.id
}

output "tf_locks_runtime_table_arn" {
  description = "ARN of the DynamoDB table used for runtime Terraform state locking."
  value       = module.tf_locks_runtime.arn
}

# ── Bootstrap layer (read-only references) ──────────────────────────────────
# These expose ARNs of bootstrap-created resources so that workload stacks can
# reference them via terraform_remote_state rather than hard-coding names.

output "tf_state_root_bucket" {
  description = "Name of the S3 bucket holding root Terraform state (bootstrap-managed)."
  value       = data.aws_s3_bucket.tf_state_root.id
}

output "tf_state_root_bucket_arn" {
  description = "ARN of the root Terraform state bucket (bootstrap-managed)."
  value       = data.aws_s3_bucket.tf_state_root.arn
}

output "tf_locks_root_table" {
  description = "Name of the DynamoDB table for root Terraform state locking (bootstrap-managed)."
  value       = data.aws_dynamodb_table.tf_locks_root.name
}

output "tf_locks_root_table_arn" {
  description = "ARN of the root Terraform state lock table (bootstrap-managed)."
  value       = data.aws_dynamodb_table.tf_locks_root.arn
}

output "bootstrap_registry_table" {
  description = "Name of the DynamoDB registry table listing all bootstrap-created resources."
  value       = data.aws_dynamodb_table.bootstrap_registry.name
}

output "bootstrap_registry_table_arn" {
  description = "ARN of the bootstrap registry table."
  value       = data.aws_dynamodb_table.bootstrap_registry.arn
}

output "platform_admin_role_arn" {
  description = "ARN of the platform-admin IAM role (bootstrap-managed)."
  value       = data.aws_iam_role.platform_admin.arn
}

output "platform_events_topic_arn" {
  description = "ARN of the platform events SNS topic (bootstrap-managed)."
  value       = data.aws_sns_topic.platform_events.arn
}
