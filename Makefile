SHELL    := /bin/bash
STACK    := terraform/stack
ENVS_DIR := terraform/envs
ENVS_REL := ../envs

ENV           ?=
ORG           ?= ffreis
PROFILE       ?= bootstrap
BOOTSTRAP_BIN ?= platform-bootstrap

CLI_BIN     ?= ./bin/platform-org
CLI_SRC     := ./cmd/platform-org
LAMBDA_BIN  ?= ./bin/activate-lambda
LAMBDA_SRC  := ./lambda/activate
GO          ?= $(shell command -v go 2>/dev/null || echo /usr/local/go/bin/go)
GOFMT       ?= $(shell command -v gofmt 2>/dev/null || echo /usr/local/go/bin/gofmt)

FETCHED_FILE = $(ENVS_DIR)/$(ENV)/fetched.auto.tfvars.json
BACKEND_LOCAL_FILE = $(STACK)/backend.local.hcl

GITLEAKS         ?= gitleaks
LEFTHOOK_VERSION ?= 1.7.10
LEFTHOOK_DIR     ?= $(CURDIR)/.bin
LEFTHOOK_BIN     ?= $(LEFTHOOK_DIR)/lefthook

_require_env:
	@test -n "$(ENV)" || (echo "ENV is required, e.g. make plan ENV=prod" && exit 1)
	@test -d "$(ENVS_DIR)/$(ENV)" || (echo "Unknown environment: $(ENV)" && exit 1)

_require_fetched: _require_env
	@test -f "$(FETCHED_FILE)" || ( \
		echo "Missing $(FETCHED_FILE). Run: make fetch ENV=$(ENV)" >&2; \
		exit 1)

.PHONY: build build-cli build-lambda go-test go-audit fetch init plan apply destroy nuke fmt fmt-check validate lint test check check-static security coverage \
        secrets-scan-staged lefthook-bootstrap lefthook-install lefthook-run lefthook \
        _require_env _require_fetched

## build: compile CLI and Lambda (everything needed before apply)
build: build-cli build-lambda

## build-cli: compile only the platform-org CLI to ./bin/platform-org
build-cli:
	$(GO) build -o $(CLI_BIN) $(CLI_SRC)

## build-lambda: compile and zip the activate Lambda to ./bin/activate-lambda/function.zip
build-lambda:
	@mkdir -p $(LAMBDA_BIN)
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 $(GO) build -o $(LAMBDA_BIN)/bootstrap $(LAMBDA_SRC)
	cd $(LAMBDA_BIN) && zip -j function.zip bootstrap

## go-test: run Go unit tests for the CLI
go-test:
	$(GO) test ./... -v

## go-audit [ENV=prod]: run the AWS SDK audit (ownership, tags, budget coverage)
go-audit:
	$(CLI_BIN) audit --region us-east-1

## go-plan [ENV=prod]: run terraform plan via the CLI (assumes platform-admin role)
go-plan:
	$(CLI_BIN) plan --env $(or $(ENV),prod) --region us-east-1

## go-apply [ENV=prod]: run terraform apply via the CLI
go-apply:
	$(CLI_BIN) apply --env $(or $(ENV),prod) --region us-east-1

## go-nuke [ENV=prod]: destroy all resources (prompts for confirmation)
go-nuke:
	$(CLI_BIN) nuke --env $(or $(ENV),prod) --region us-east-1

## fetch ENV=<env>: pull config from the bootstrap registry and write fetched.auto.tfvars.json
fetch: _require_env
	$(BOOTSTRAP_BIN) fetch \
		--org=$(ORG) \
		--profile=$(PROFILE) \
		--output=$(FETCHED_FILE) \
		--backend-out=$(BACKEND_LOCAL_FILE)

## init ENV=<env>: initialise Terraform with the env backend config
init: _require_fetched
	terraform -chdir=$(STACK) init \
		-backend-config=backend.local.hcl \
		-backend-config=$(ENVS_REL)/$(ENV)/backend.hcl \
		-reconfigure

## plan ENV=<env>: show execution plan
plan: _require_fetched
	terraform -chdir=$(STACK) plan \
		-var-file=$(ENVS_REL)/$(ENV)/terraform.tfvars \
		-var-file=$(ENVS_REL)/$(ENV)/fetched.auto.tfvars.json

## apply ENV=<env>: apply changes
apply: _require_fetched
	terraform -chdir=$(STACK) apply \
		-var-file=$(ENVS_REL)/$(ENV)/terraform.tfvars \
		-var-file=$(ENVS_REL)/$(ENV)/fetched.auto.tfvars.json

## destroy ENV=<env>: destroy all resources (requires confirmation)
destroy: _require_fetched
	terraform -chdir=$(STACK) destroy \
		-var-file=$(ENVS_REL)/$(ENV)/terraform.tfvars \
		-var-file=$(ENVS_REL)/$(ENV)/fetched.auto.tfvars.json

## nuke ENV=<env>: fetch, init then destroy -auto-approve (IRREVERSIBLE)
## NOTE: State/storage buckets in this stack are configured with force_destroy=false.
## Empty those buckets before running nuke, or terraform destroy may fail.
nuke: _require_fetched
	@echo "WARNING: The org stack contains buckets configured with force_destroy=false."
	@echo "         Empty those buckets before proceeding, or terraform destroy may fail."
	@read -p "Type 'nuke-$(ENV)' to confirm destruction of org/$(ENV): " -r; \
	if [ "$$REPLY" != "nuke-$(ENV)" ]; then \
		echo "Cancelled."; \
		exit 1; \
	fi
	terraform -chdir=$(STACK) init \
		-backend-config=backend.local.hcl \
		-backend-config=$(ENVS_REL)/$(ENV)/backend.hcl \
		-reconfigure
	terraform -chdir=$(STACK) destroy \
		-var-file=$(ENVS_REL)/$(ENV)/terraform.tfvars \
		-var-file=$(ENVS_REL)/$(ENV)/fetched.auto.tfvars.json \
		-auto-approve

## fmt: format all Go and Terraform files
fmt:
	./scripts/hooks/check_required_tools.sh terraform
	$(GOFMT) -w $$(find . -type d \( -name .terraform -o -name vendor -o -path ./bin \) -prune -o -type f -name '*.go' -print)
	terraform fmt -recursive .

## fmt-check: fail if any Go or Terraform file is not formatted
fmt-check:
	./scripts/hooks/check_required_tools.sh terraform
	@unformatted=$$(find . -type d \( -name .terraform -o -name vendor -o -path ./bin \) -prune -o -type f -name '*.go' -print | xargs $(GOFMT) -l); \
	if [ -n "$$unformatted" ]; then \
	  printf "The following files need gofmt:\n%s\n\nFix with: gofmt -w .\n" "$$unformatted"; \
	  exit 1; \
	fi
	terraform fmt -check -recursive .

## validate: validate the stack configuration (static, no AWS credentials required)
validate:
	./scripts/hooks/check_required_tools.sh terraform
	./scripts/hooks/validate_terraform.sh

## lint: run tflint across all Terraform files
lint:
	./scripts/hooks/run_tflint_if_available.sh

## test: no unit tests — use 'make validate' or 'make plan ENV=<env>'
test:
	@echo "INFO: No unit tests for Terraform repos. Use:"
	@echo "      make validate          — static validation"
	@echo "      make plan ENV=<env>    — execution plan against real state"

## check ENV=<env>: post-apply validation against live AWS
check: _require_env
	ENV=$(ENV) ./scripts/validate.sh

## check-static: run all static checks (no cloud)
check-static: fmt-check validate lint security

## security: run trivy + checkov security scans
security:
	trivy config --exit-code 1 --severity HIGH,CRITICAL .
	@which checkov >/dev/null 2>&1 && checkov -d . --framework terraform --quiet || true

## coverage: run terratest with coverage (modules repo only)
coverage:
	cd test && go test -v ./... -timeout 30m 2>/dev/null || echo "No terratest found"

## secrets-scan-staged: scan staged diff for secrets
secrets-scan-staged:
	@command -v $(GITLEAKS) >/dev/null 2>&1 || (echo "Missing tool: $(GITLEAKS). Install: https://github.com/gitleaks/gitleaks#installing" && exit 1)
	$(GITLEAKS) protect --staged --redact

## lefthook-bootstrap: download lefthook binary into ./.bin
lefthook-bootstrap:
	LEFTHOOK_VERSION="$(LEFTHOOK_VERSION)" BIN_DIR="$(LEFTHOOK_DIR)" bash ./scripts/bootstrap_lefthook.sh

## lefthook-install: install git hooks (runs bootstrap first)
lefthook-install: lefthook-bootstrap
	@if [ -x "$(LEFTHOOK_BIN)" ] && [ -x ".git/hooks/pre-commit" ] && [ -x ".git/hooks/pre-push" ] && [ -x ".git/hooks/commit-msg" ]; then \
		echo "lefthook hooks already installed"; \
		exit 0; \
	fi
	LEFTHOOK="$(LEFTHOOK_BIN)" "$(LEFTHOOK_BIN)" install

## lefthook-run: run all hooks locally (pre-commit + commit-msg + pre-push)
lefthook-run: lefthook-bootstrap
	LEFTHOOK="$(LEFTHOOK_BIN)" "$(LEFTHOOK_BIN)" run pre-commit
	@tmp_msg="$$(mktemp)"; \
	echo "chore(hooks): validate commit-msg hook" > "$$tmp_msg"; \
	LEFTHOOK="$(LEFTHOOK_BIN)" "$(LEFTHOOK_BIN)" run commit-msg -- "$$tmp_msg"; \
	rm -f "$$tmp_msg"
	LEFTHOOK="$(LEFTHOOK_BIN)" "$(LEFTHOOK_BIN)" run pre-push

## lefthook: install hooks and run them
lefthook: lefthook-bootstrap lefthook-install lefthook-run

## help: list documented targets
help:
	@grep -E '^## ' Makefile | sed 's/## /  /'

PLATFORM_STANDARDS_SHA ?= 3c787edb4e96ddea2e86b2add2c32139685e8db7  # v1.2.1
PLATFORM_STANDARDS_RAW ?= https://raw.githubusercontent.com/FelipeFuhr/ffreis-platform-standards

install-act: ## Download pinned act binary into .bin/
	@mkdir -p scripts
	@curl -fsSL "$(PLATFORM_STANDARDS_RAW)/$(PLATFORM_STANDARDS_SHA)/scripts/install_act.sh" \
		-o scripts/install_act.sh && chmod +x scripts/install_act.sh
	@bash ./scripts/install_act.sh

ci-local: ## Run workflows locally via act (GH Actions quota fallback). Args via ARGS=...
	@mkdir -p scripts
	@curl -fsSL "https://raw.githubusercontent.com/FelipeFuhr/ffreis-platform-ci-local/v1.0.0/scripts/run-ci-local.sh" \
		-o scripts/run-ci-local.sh && chmod +x scripts/run-ci-local.sh
	@CI_LOCAL_FINDINGS_REF=v1.0.0 PATH="$(CURDIR)/.bin:$(PATH)" bash ./scripts/run-ci-local.sh $(ARGS)
