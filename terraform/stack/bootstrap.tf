# ---------------------------------------------------------------------------
# Bootstrap layer — read-only visibility
#
# Bootstrap resources are created by the Go `platform-bootstrap` CLI, not by
# Terraform. These data sources bring them into the Terraform graph as
# read-only references, enabling:
#
#   - cross-stack outputs (other stacks can reference these ARNs from
#     platform-org remote state rather than hard-coding names)
#   - audit inventory (this apply fails fast if any bootstrap resource
#     has been accidentally deleted or renamed)
#   - the resource group below provides a live AWS Console / API view
#
# Nothing in this file manages or modifies bootstrap resources.
# ---------------------------------------------------------------------------

data "aws_s3_bucket" "tf_state_root" {
  bucket = "${var.org}-tf-state-root"
}

data "aws_dynamodb_table" "tf_locks_root" {
  name = "${var.org}-tf-locks-root"
}

data "aws_dynamodb_table" "bootstrap_registry" {
  name = "${var.org}-bootstrap-registry"
}

data "aws_iam_role" "platform_admin" {
  name = "platform-admin"
}

data "aws_sns_topic" "platform_events" {
  name = "${var.org}-platform-events"
}

# ---------------------------------------------------------------------------
# Cost allocation tags are no longer managed by Terraform.
# They are activated directly via the AWS Cost Explorer API by:
#   - 'platform-org activate'  (manual CLI path)
#   - the activate Lambda      (auto path, ~25h after apply)
# ---------------------------------------------------------------------------
# Resource Group: bootstrap layer
#
# Queries all resources tagged Layer=bootstrap. Provides a single-pane
# view in the AWS Console, CloudTrail queries, and the Resource Groups
# Tagging API — useful for auditing and bulk operations.
# ---------------------------------------------------------------------------

resource "aws_resourcegroups_group" "bootstrap" {
  name        = "${var.org}-bootstrap-layer"
  description = "Bootstrap layer resources - managed by platform-bootstrap CLI"

  resource_query {
    type = "TAG_FILTERS_1_0"
    query = jsonencode({
      ResourceTypeFilters = ["AWS::AllSupported"]
      TagFilters = [{
        Key    = "Layer"
        Values = ["bootstrap"]
      }]
    })
  }

  tags = merge(local.common_tags, {
    Layer = "platform-org"
    Stack = "platform-org"
  })
}
