# AGENTS.md — platform-org

Terraform stack + companion Go CLI for the AWS platform **organization** layer
(the layer after `platform-bootstrap`). The CLI is Cobra-based; entrypoint
`cmd/platform-org/main.go`, commands in `cmd/*.go`, shared deps built once in
`cmd/root.go` `PersistentPreRunE` and read from the package-scoped `d` struct.

## Commands

Terraform lifecycle: `plan`, `apply`, `nuke`, `purge`, `activate`, `tempuser`.
Diagnostics: `audit`, `doctor`. Read-only insight (added 2026-05-31): `cost`,
`accounts` (alias `org`), `resources` (alias `inventory`) — all support `--json`.

## Non-obvious constraints

- **AWS auth.** `--profile` or `AWS_ACCESS_KEY_ID`/`SECRET` env (no IMDS
  fallback). Caller identity is checked; unless already `assumed-role/
  platform-admin`, the CLI assumes `arn:aws:iam::<acct>:role/platform-admin`
  (root creds use the `tempuser` bridge). Assumed creds are injected into the
  terraform subprocess env. Local-only commands (e.g. `version`) set the
  `local` annotation to skip credential loading.
- **Cost Explorer / Budgets are us-east-1 only.** The `cost` command builds
  dedicated CE + Budgets clients pinned to us-east-1 regardless of `--region`.
  CE charges ~$0.01 per call; `cost` runs on demand — do not wire it into a
  tight loop. Tag breakdowns are empty until cost-allocation tags are *active*
  (`platform-org activate`, then ~24h warm-up).
- **`resources` is region-scoped.** It uses the regional Resource Groups
  Tagging API, so it lists only resources in `--region`.

## Shared library dependency

The `cost`/`accounts`/`resources` commands are thin renderers over
[`ffreis-platform-inventory`](https://github.com/FelipeFuhr/ffreis-platform-inventory)
(`pkg/cost`, `pkg/org`, `pkg/resources`) — the canonical, importable home for
the fleet's responsibility-keyed read logic, shared with future dashboard
Lambdas. `go.mod` requires it at a pinned GitHub pseudo-version, and its transitive
`github.com/ffreis/platform-cli` is `replace`d to the
`github.com/FelipeFuhr/ffreis-platform-cli` pseudo-version (the same pattern
`ffreis-flemming-infra` uses). Fetching needs `GOPRIVATE=github.com/FelipeFuhr/*`.

`audit` and `doctor` both support `--json` (machine-readable contract; see
`cmd/audit_json.go` for the audit projection). Deliberately NOT done: the older
duplicated audit/inventory/tfexec logic in `package cmd` is NOT refactored onto
`platform-cli` — it can't be a mechanical dedup. platform-org's `parseARN` and
bootstrap/UNOWNED/terraform classification differ from platform-cli's
special-cased `ResourceTypeFromARN` + OWNED/OTHER_MANAGED model, and the audit
internals are deeply mocked across the test suite, so adopting the shared engine
is a redesign (it changes audit's output), not a quick swap.

## Build / test

```sh
make build        # build-cli + build-lambda
make test         # go test -race -shuffle=on ./...
make check-static # fmt-check + validate + lint + security (lint needs golangci-lint)
```
