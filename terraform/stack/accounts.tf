resource "aws_organizations_account" "environments" {
  for_each = var.accounts

  name      = each.key
  email     = each.value.email
  parent_id = aws_organizations_organizational_unit.environments.id

  role_name                  = "OrganizationAccountAccessRole"
  iam_user_access_to_billing = "ALLOW"

  # Prevent accidental account closure on terraform destroy.
  close_on_deletion = false

  lifecycle {
    # role_name is only applied at creation time; ignore drift on subsequent plans.
    ignore_changes = [role_name]
  }
}
