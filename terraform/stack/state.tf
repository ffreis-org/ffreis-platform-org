# ---------------------------------------------------------------------------
# Runtime state backend — used by workload stacks (separate from root).
# Uses the shared platform-terraform-modules/modules/s3-bucket and
# dynamodb-table modules to enforce org-wide best practices automatically:
#   - public access block (all 4 settings)
#   - AES-256 server-side encryption
#   - TLS-only bucket policy (deny HTTP)
#   - versioning
#   - DynamoDB PITR
#   - DynamoDB SSE
# ---------------------------------------------------------------------------

module "tf_state_runtime" {
  source = "git::https://github.com/FelipeFuhr/ffreis-platform-terraform-modules.git//modules/s3-bucket?ref=f828c757a5c837a33675e0f383f988f93d4f3387"

  bucket                = "${var.org}-tf-state-runtime"
  versioning_enabled    = true
  sse_algorithm         = "AES256"
  logging_target_bucket = ""
  force_destroy         = false

  # Expire noncurrent state versions after 90 days to contain storage costs.
  lifecycle_rules = [
    {
      id                                 = "expire-noncurrent-state"
      enabled                            = true
      noncurrent_version_expiration_days = 90
    },
  ]

  tags = merge(local.common_tags, {
    Name    = "${var.org}-tf-state-runtime"
    Purpose = "terraform-state"
    Tier    = "runtime"
    Layer   = "platform-org"
    Stack   = "platform-org"
  })
}

module "tf_locks_runtime" {
  source = "git::https://github.com/FelipeFuhr/ffreis-platform-terraform-modules.git//modules/dynamodb-table?ref=f828c757a5c837a33675e0f383f988f93d4f3387"

  name     = "${var.org}-tf-locks-runtime"
  hash_key = "LockID"

  tags = merge(local.common_tags, {
    Name    = "${var.org}-tf-locks-runtime"
    Purpose = "terraform-locks"
    Tier    = "runtime"
    Layer   = "platform-org"
    Stack   = "platform-org"
  })
}

# ---------------------------------------------------------------------------
# Bootstrap state buckets — created by platform-bootstrap CLI and adopted
# here for tag management. These three buckets hold Terraform state for the
# entire platform (root=bootstrap layer, prod/dev=workload environments).
#
# Only tags are managed by Terraform. All other bucket configuration
# (versioning, encryption, public-access-block) is owned by the bootstrap
# CLI's EnsureStateBucket — ignore_changes prevents drift on those attributes.
# ---------------------------------------------------------------------------

import {
  to = aws_s3_bucket.tf_state_root
  id = "${var.org}-tf-state-root"
}

resource "aws_s3_bucket" "tf_state_root" {
  bucket        = "${var.org}-tf-state-root"
  force_destroy = false

  lifecycle {
    prevent_destroy = true
    ignore_changes  = [object_lock_enabled]
  }

  tags = merge(local.common_tags, {
    Name    = "${var.org}-tf-state-root"
    Purpose = "terraform-state"
    Tier    = "root"
    Layer   = "bootstrap"
    Stack   = "bootstrap"
  })
}

import {
  to = aws_s3_bucket.tf_state_prod
  id = "${var.org}-tf-state-prod"
}

resource "aws_s3_bucket" "tf_state_prod" {
  bucket        = "${var.org}-tf-state-prod"
  force_destroy = false

  lifecycle {
    prevent_destroy = true
    ignore_changes  = [object_lock_enabled]
  }

  tags = merge(local.common_tags, {
    Name    = "${var.org}-tf-state-prod"
    Purpose = "terraform-state"
    Tier    = "prod"
    Layer   = "platform-org"
    Stack   = "platform-org"
  })
}

import {
  to = aws_s3_bucket.tf_state_dev
  id = "${var.org}-tf-state-dev"
}

resource "aws_s3_bucket" "tf_state_dev" {
  bucket        = "${var.org}-tf-state-dev"
  force_destroy = false

  lifecycle {
    prevent_destroy = true
    ignore_changes  = [object_lock_enabled]
  }

  # Environment and lifecycle override: this is the dev state bucket.
  tags = merge(local.common_tags, {
    Name           = "${var.org}-tf-state-dev"
    Purpose        = "terraform-state"
    Tier           = "dev"
    Layer          = "platform-org"
    Stack          = "platform-org"
    Environment    = "dev"
    LifecycleState = "development"
  })
}
