# ---------------------------------------------------------------------------
# Service Control Policies attached to the environments OU.
# ---------------------------------------------------------------------------

# -- Deny IAM user creation -------------------------------------------------

data "aws_iam_policy_document" "deny_iam_user_creation" {
  statement {
    sid       = "DenyIAMUserCreation"
    effect    = "Deny"
    actions   = ["iam:CreateUser"]
    resources = ["*"]
  }
}

resource "aws_organizations_policy" "deny_iam_user_creation" {
  name        = "deny-iam-user-creation"
  description = "Prevents creation of long-lived IAM users; use IAM roles instead."
  type        = "SERVICE_CONTROL_POLICY"
  content     = data.aws_iam_policy_document.deny_iam_user_creation.json

  depends_on = [aws_organizations_organization.this]
}

resource "aws_organizations_policy_attachment" "deny_iam_user_creation" {
  policy_id = aws_organizations_policy.deny_iam_user_creation.id
  target_id = aws_organizations_organizational_unit.environments.id
}

# -- Deny disabling CloudTrail ----------------------------------------------

data "aws_iam_policy_document" "deny_disable_cloudtrail" {
  statement {
    sid    = "DenyDisableCloudTrail"
    effect = "Deny"
    actions = [
      "cloudtrail:DeleteTrail",
      "cloudtrail:StopLogging",
      "cloudtrail:UpdateTrail",
      "cloudtrail:PutEventSelectors",
    ]
    resources = ["*"]
  }
}

resource "aws_organizations_policy" "deny_disable_cloudtrail" {
  name        = "deny-disable-cloudtrail"
  description = "Prevents tampering with CloudTrail logging in member accounts."
  type        = "SERVICE_CONTROL_POLICY"
  content     = data.aws_iam_policy_document.deny_disable_cloudtrail.json

  depends_on = [aws_organizations_organization.this]
}

resource "aws_organizations_policy_attachment" "deny_disable_cloudtrail" {
  policy_id = aws_organizations_policy.deny_disable_cloudtrail.id
  target_id = aws_organizations_organizational_unit.environments.id
}

# -- Deny leaving the organisation ------------------------------------------

data "aws_iam_policy_document" "deny_leave_org" {
  statement {
    sid       = "DenyLeaveOrganization"
    effect    = "Deny"
    actions   = ["organizations:LeaveOrganization"]
    resources = ["*"]
  }
}

resource "aws_organizations_policy" "deny_leave_org" {
  name        = "deny-leave-organization"
  description = "Prevents member accounts from detaching themselves from the organisation."
  type        = "SERVICE_CONTROL_POLICY"
  content     = data.aws_iam_policy_document.deny_leave_org.json

  depends_on = [aws_organizations_organization.this]
}

resource "aws_organizations_policy_attachment" "deny_leave_org" {
  policy_id = aws_organizations_policy.deny_leave_org.id
  target_id = aws_organizations_organizational_unit.environments.id
}
