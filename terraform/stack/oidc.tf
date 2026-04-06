# ---------------------------------------------------------------------------
# GitHub Actions OIDC provider
#
# Created once in the management account. All projects reference it via a
# data source. The thumbprint is fetched dynamically from GitHub's OIDC
# endpoint so it remains valid after certificate rotations.
# ---------------------------------------------------------------------------
data "tls_certificate" "github_oidc" {
  url = "https://token.actions.githubusercontent.com/.well-known/openid-configuration"
}

resource "aws_iam_openid_connect_provider" "github" {
  url             = "https://token.actions.githubusercontent.com"
  client_id_list  = ["sts.amazonaws.com"]
  thumbprint_list = [data.tls_certificate.github_oidc.certificates[length(data.tls_certificate.github_oidc.certificates) - 1].sha1_fingerprint]

  tags = merge(local.common_tags, {
    Name  = "github-actions-oidc"
    Layer = "platform-org"
  })
}
