variable "aws_region" {
  description = "AWS region for the management account."
  type        = string
  default     = "us-east-1"
}

variable "aws_profile" {
  description = "AWS CLI profile to use for the management account. Set to null when using injected environment credentials (AWS_ACCESS_KEY_ID etc.)."
  type        = string
  default     = null
}

variable "org" {
  description = "Short org identifier used in resource naming (e.g. ffreis)."
  type        = string
}

variable "accounts" {
  description = "Member accounts to create under the environments OU. Map key is the account name."
  type = map(object({
    email = string
  }))
}

variable "budget_alert_email" {
  description = "Email address that receives budget alert notifications. Supplied via fetched.auto.tfvars.json — never committed to source control."
  type        = string
}

variable "budget_alert_threshold_usd" {
  description = "Monthly spend limit in USD. Alerts fire at 80% and 100% actual, and 100% forecasted."
  type        = number
  default     = 20
  validation {
    condition     = var.budget_alert_threshold_usd > 0
    error_message = "Budget threshold must be greater than 0."
  }
}

variable "project" {
  description = "Project tag value applied to all managed resources."
  type        = string
  default     = "platform"
}

variable "environment" {
  description = "Environment tag value applied to all managed resources."
  type        = string
  default     = "prod"
}

variable "tags" {
  description = "Additional tags applied to all resources."
  type        = map(string)
  default     = {}
}
