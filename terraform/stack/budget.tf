# ── Org-wide budgets ─────────────────────────────────────────────────────────
# INTENTIONALLY UNFILTERED. This stack runs in the AWS Organization payer
# account, so a budget here with no cost_filter tracks TOTAL spend across every
# member account — present and future — via consolidated billing. These are the
# org-level safety net: they catch untagged spend and any new project before it
# gets its own budget, so nothing untracked can spiral.
#
# Per-project attribution lives elsewhere: each product's infra stack
# (petlook-infra, ffreis-flemming-infra, ffreis-platform-shared-infra, …) owns a
# CostCenter-tag-scoped monthly budget. Those budgets and these are complementary
# — do NOT add cost filters here, and do NOT add more unfiltered budgets in the
# product stacks (that was the 2026-06 bug: every product budget tracked the
# whole account and tripped together).
#
# Two tiers:
#   - admin   ($30): early tripwire, ~2.5x normal fleet spend (minimal-cost mode ~$10-12/mo)
#   - ceiling ($60): higher hard ceiling for a genuine runaway

# Tier 1 — early tripwire.
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

# Tier 2 — higher hard ceiling for a genuine runaway across the org.
resource "aws_budgets_budget" "platform_ceiling" {
  name         = "${var.org}-platform-ceiling-budget"
  budget_type  = "COST"
  limit_amount = tostring(var.budget_ceiling_threshold_usd)
  limit_unit   = "USD"
  time_unit    = "MONTHLY"

  # Warn at 80% so there is time to react before the ceiling is hit.
  notification {
    comparison_operator        = "GREATER_THAN"
    threshold                  = 80
    threshold_type             = "PERCENTAGE"
    notification_type          = "ACTUAL"
    subscriber_email_addresses = [var.budget_alert_email]
  }

  # Hard alert at 100% of the ceiling.
  notification {
    comparison_operator        = "GREATER_THAN"
    threshold                  = 100
    threshold_type             = "PERCENTAGE"
    notification_type          = "ACTUAL"
    subscriber_email_addresses = [var.budget_alert_email]
  }

  # Forward-looking alert on the ceiling.
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
