resource "aws_budgets_budget" "platform_admin" {
  name         = "${var.org}-platform-admin-budget"
  budget_type  = "COST"
  limit_amount = tostring(var.budget_alert_threshold_usd)
  limit_unit   = "USD"
  time_unit    = "MONTHLY"

  # Warn at 80% so there is time to react before the limit is hit.
  notification {
    comparison_operator        = "GREATER_THAN"
    threshold                  = 80
    threshold_type             = "PERCENTAGE"
    notification_type          = "ACTUAL"
    subscriber_email_addresses = [var.budget_alert_email]
  }

  # Hard alert at 100% of the limit.
  notification {
    comparison_operator        = "GREATER_THAN"
    threshold                  = 100
    threshold_type             = "PERCENTAGE"
    notification_type          = "ACTUAL"
    subscriber_email_addresses = [var.budget_alert_email]
  }

  # Forward-looking alert: notify when the forecasted spend is on track to
  # exceed the limit even if actual spend has not yet reached it.
  notification {
    comparison_operator        = "GREATER_THAN"
    threshold                  = 100
    threshold_type             = "PERCENTAGE"
    notification_type          = "FORECASTED"
    subscriber_email_addresses = [var.budget_alert_email]
  }

  tags = merge(local.common_tags, {
    Layer = "platform-org"
    Stack = "platform-org"
  })
}
