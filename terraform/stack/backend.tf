terraform {
  # Backend values are NOT committed — they identify real infrastructure.
  #
  # Create a local (gitignored) file: terraform/stack/backend.local.hcl
  #   bucket         = "<your-root-tf-state-bucket>"
  #   dynamodb_table = "<your-root-tf-lock-table>"
  #   region         = "<region>"
  #   key            = "platform-org/terraform.tfstate"
  #
  # Then initialise with:
  #   terraform init -backend-config=backend.local.hcl
  backend "s3" {
    encrypt = true
  }
}
