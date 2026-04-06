# ---------------------------------------------------------------------------
# Auto-activation infrastructure
#
# The activate Lambda (Go, imports internal/activation) is the zero-touch path
# for activating cost allocation tags. It is triggered by a one-time
# EventBridge Scheduler rule created by 'platform-org apply' ~25h after the
# first successful apply, giving AWS Cost Explorer time to discover fresh tag
# keys.
#
# The CLI 'platform-org activate' is the manual path — it calls the same
# activation logic directly via the AWS SDK.
#
# On success the Lambda publishes a notification to the platform events SNS
# topic (which has an email subscription set up by bootstrap). On failure it
# returns an error so EventBridge Scheduler can retry.
# ---------------------------------------------------------------------------

# Current account identity (used for ARN construction and least-privilege scoping)
data "aws_caller_identity" "current" {}

locals {
  common_tags = merge({
    ManagedBy   = "terraform"
    Stack       = "platform-org"
    Project     = var.project
    Environment = var.environment
  }, var.tags)
}

# ---------------------------------------------------------------------------
# Lambda execution role
# ---------------------------------------------------------------------------

resource "aws_iam_role" "activate_lambda" {
  name = "${var.org}-activate-cost-tags"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Action    = "sts:AssumeRole"
      Effect    = "Allow"
      Principal = { Service = "lambda.amazonaws.com" }
    }]
  })

  tags = merge(local.common_tags, { Layer = "platform-org" })
}

resource "aws_iam_role_policy" "activate_lambda" {
  name = "activate-cost-tags"
  role = aws_iam_role.activate_lambda.id

  #checkov:skip=CKV_AWS_355:CE cost-allocation-tag actions and logs:CreateLogGroup do not support resource-level restrictions; AWS rejects any ARN other than '*' for these.
  #checkov:skip=CKV_AWS_290:logs:CreateLogGroup does not support resource-level restrictions (AWS limitation); all other write actions are scoped to specific resources.

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid    = "CostExplorer"
        Effect = "Allow"
        Action = ["ce:UpdateCostAllocationTagsStatus", "ce:ListCostAllocationTags"]
        # AWS Cost Explorer does not support resource-level restrictions; '*' is required.
        Resource = "*"
      },
      {
        Sid    = "LogsCreateGroup"
        Effect = "Allow"
        Action = "logs:CreateLogGroup"
        # AWS does not support resource-level restrictions for CreateLogGroup.
        Resource = "*"
      },
      {
        Sid    = "LogsWrite"
        Effect = "Allow"
        Action = ["logs:CreateLogStream", "logs:PutLogEvents"]
        # Scoped to this Lambda's log group to follow least-privilege.
        Resource = "arn:aws:logs:${var.aws_region}:${data.aws_caller_identity.current.account_id}:log-group:/aws/lambda/${var.org}-activate-cost-tags:*"
      },
      {
        Sid      = "SNSNotify"
        Effect   = "Allow"
        Action   = "sns:Publish"
        Resource = data.aws_sns_topic.platform_events.arn
      }
    ]
  })
}

# ---------------------------------------------------------------------------
# CloudWatch log group (explicit, so retention is controlled by Terraform)
# ---------------------------------------------------------------------------

resource "aws_cloudwatch_log_group" "activate_lambda" {
  name              = "/aws/lambda/${var.org}-activate-cost-tags"
  retention_in_days = 365
  #checkov:skip=CKV_AWS_158:No KMS key is provisioned in this stack; log group contains no sensitive data (only activation status messages).
  tags = merge(local.common_tags, { Layer = "platform-org" })
}

# ---------------------------------------------------------------------------
# Lambda function
# The ZIP is produced by 'make build-lambda' before terraform apply.
# ---------------------------------------------------------------------------

resource "aws_lambda_function" "activate_cost_tags" {
  function_name    = "${var.org}-activate-cost-tags"
  role             = aws_iam_role.activate_lambda.arn
  filename         = "${path.module}/../../bin/activate-lambda/function.zip"
  source_code_hash = filebase64sha256("${path.module}/../../bin/activate-lambda/function.zip")
  handler          = "bootstrap"
  runtime          = "provided.al2023"
  timeout          = 30

  # One concurrent execution is sufficient: the schedule fires once and
  # EventBridge Scheduler retries on non-nil error; throttling is not a concern.
  reserved_concurrent_executions = 1

  #checkov:skip=CKV_AWS_117:This Lambda calls public AWS APIs (CE, SNS) and does not require VPC placement; placing it in a VPC would require a NAT gateway with no security benefit.
  #checkov:skip=CKV_AWS_173:Environment variable contains only the SNS topic ARN (non-sensitive); no KMS key is provisioned in this stack.
  #checkov:skip=CKV_AWS_116:EventBridge Scheduler handles retries when the Lambda returns a non-nil error; a separate DLQ would be redundant.
  #checkov:skip=CKV_AWS_272:Internal first-party Lambda deployed from the same repository; code signing adds CI/CD complexity without meaningful security benefit for this use case.

  tracing_config {
    mode = "Active"
  }

  environment {
    variables = {
      PLATFORM_EVENTS_TOPIC_ARN = data.aws_sns_topic.platform_events.arn
    }
  }

  depends_on = [aws_cloudwatch_log_group.activate_lambda]

  tags = merge(local.common_tags, { Layer = "platform-org" })
}

# ---------------------------------------------------------------------------
# EventBridge Scheduler infrastructure
#
# The schedule GROUP is permanent (Terraform-managed). The one-time schedule
# RULE is ephemeral — created and updated by 'platform-org apply' via the
# AWS SDK after each successful terraform apply, targeting T+25h.
# ---------------------------------------------------------------------------

resource "aws_scheduler_schedule_group" "platform_org" {
  name = "${var.org}-platform-org"
  tags = merge(local.common_tags, { Layer = "platform-org" })
}

# IAM role allowing EventBridge Scheduler to invoke the Lambda
resource "aws_iam_role" "scheduler_invoke_lambda" {
  name = "${var.org}-scheduler-invoke-activate"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Action    = "sts:AssumeRole"
      Effect    = "Allow"
      Principal = { Service = "scheduler.amazonaws.com" }
      Condition = {
        StringEquals = {
          "aws:SourceAccount" = data.aws_caller_identity.current.account_id
        }
      }
    }]
  })

  tags = merge(local.common_tags, { Layer = "platform-org" })
}

resource "aws_iam_role_policy" "scheduler_invoke_lambda" {
  name = "invoke-activate-lambda"
  role = aws_iam_role.scheduler_invoke_lambda.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Sid      = "InvokeLambda"
      Effect   = "Allow"
      Action   = "lambda:InvokeFunction"
      Resource = aws_lambda_function.activate_cost_tags.arn
    }]
  })
}

# ---------------------------------------------------------------------------
# Outputs used by 'platform-org apply' to construct schedule ARNs without
# additional API calls.
# ---------------------------------------------------------------------------

output "activate_lambda_arn" {
  description = "ARN of the cost-tag activation Lambda."
  value       = aws_lambda_function.activate_cost_tags.arn
}

output "scheduler_group_name" {
  description = "EventBridge Scheduler group name used for the one-time activation schedule."
  value       = aws_scheduler_schedule_group.platform_org.name
}

output "scheduler_invoke_role_arn" {
  description = "ARN of the IAM role used by EventBridge Scheduler to invoke the activation Lambda."
  value       = aws_iam_role.scheduler_invoke_lambda.arn
}
