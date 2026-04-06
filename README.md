# platform-org

Terraform stack and companion CLI for managing the platform organization layer.

This repo is the next layer after `platform-bootstrap`:

1. `platform-bootstrap init` creates the root bootstrap resources
2. `platform-bootstrap fetch` writes the generated Terraform inputs for this repo
3. `platform-org` runs `terraform init`, `plan`, `apply`, `audit`, and `nuke`

## Layout

```text
platform-org/
  cmd/
    platform-org/
      main.go
  terraform/
    envs/
      prod/
        backend.hcl
        terraform.tfvars
        fetched.auto.tfvars.json   # generated, gitignored
    stack/
      backend.tf
      backend.local.hcl            # generated, gitignored
      *.tf
  scripts/
  Makefile
```

## Prerequisites

- Terraform 1.9+
- Go 1.24+ for the CLI
- AWS credentials for the management account
- The bootstrap layer must already exist

## Bootstrap dependency

This repo does not create the root bootstrap resources itself.

Before running Terraform here, create the bootstrap layer and generate the local
config files:

```sh
platform-bootstrap init \
  --org acme \
  --profile bootstrap \
  --root-email root@example.com \
  --org-dir ../platform-org
```

That writes:

- `terraform/envs/prod/fetched.auto.tfvars.json`
- `terraform/stack/backend.local.hcl`

Both files are local/generated and should not be committed.

## Local usage

```sh
make build
make fetch ENV=prod ORG=acme PROFILE=bootstrap
make init ENV=prod
make plan ENV=prod
make apply ENV=prod
```

Or use the CLI directly:

```sh
./bin/platform-org plan --env prod --region us-east-1 --profile bootstrap
./bin/platform-org apply --env prod --region us-east-1 --profile bootstrap
./bin/platform-org audit --region us-east-1 --profile bootstrap
```

## Safety notes

- `terraform/stack/backend.local.hcl` contains real backend identifiers and is gitignored
- `terraform/envs/*/fetched.auto.tfvars.json` is generated from the bootstrap registry and is gitignored
- `nuke` is destructive and intended for exceptional teardown flows

## Development

```sh
make fmt
make validate
make lint
make go-test
make check ENV=prod
```
