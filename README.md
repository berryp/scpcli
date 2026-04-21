# scpcli

A Go CLI for the [Samsung SDS Cloud Platform (SCP)](https://cloud.samsungsds.com) OpenAPI. Covers all 1,393 operations across 83 services — generated directly from the official API documentation.

> **Note:** This CLI targets Samsung Cloud Platform (SCP) v1 only. It does not work with Samsung Cloud Platform v2.

## Installation

```bash
go install github.com/berryp/scpcli/cmd/scpcli@latest
```

## Configuration

Config is read from `~/.scp/config.json` and `~/.scp/credentials.json`. Environment variables override file values.

### Config file — `~/.scp/config.json`

```json
{
  "host": "https://openapi.samsungsdscloud.com",
  "project-id": "PROJECT-xxxxxxxxxxxxxxxxxxxx",
  "user-id": "your-user-id",
  "email": "you@example.com"
}
```

### Credentials file — `~/.scp/credentials.json`

```json
{
  "auth-method": "access-key",
  "access-key": "your-access-key",
  "secret-key": "your-secret-key"
}
```

Only `access-key` auth is supported. `project-id`, `access-key`, and `secret-key` are required.

### Environment variables

All config values can be set or overridden via environment variables:

| Variable         | Description                        |
| ---------------- | ---------------------------------- |
| `SCP_HOST`       | API base URL                       |
| `SCP_PROJECT_ID` | Project ID                         |
| `SCP_USER_ID`    | User ID                            |
| `SCP_EMAIL`      | Email address                      |
| `SCP_ACCESS_KEY` | Access key for HMAC-SHA256 signing |
| `SCP_SECRET_KEY` | Secret key for HMAC-SHA256 signing |

## Usage

```
scpcli [service] [operation] [flags]
```

Global flags:

| Flag            | Description                                             |
| --------------- | ------------------------------------------------------- |
| `--output-file` | Write response to a file instead of stdout              |
| `--filter`      | yq expression to filter output (e.g. `'.items[].name'`) |

### Output filtering

`--filter` accepts any [yq](https://mikefarah.gitbook.io/yq/) expression and applies it to the JSON response before printing.

```bash
# Extract a list of names
scpcli iam list-groups --filter '.contents[].groupName'

# Filter by field value
scpcli iam list-groups --filter '.contents[] | select(.groupState == "ACTIVE")'

# Pluck a single scalar
scpcli iam detail-role --role-id ROLE-xxxx --filter '.roleName'
```

All environment variables are available inside filter expressions via `$ENV`:

```bash
export ROLE_ID=ROLE-xxxx
scpcli iam list-roles --filter '.contents[] | select(.roleId == $ENV.ROLE_ID)'
```

### Examples

```bash
# List Kafka clusters
scpcli apache-kafka list-kafka-clusters

# Get a specific virtual server
scpcli virtual-server detail-virtual-server-v3 --virtual-server-id VS-xxxx

# Create a resource (body as JSON string)
scpcli apache-kafka create-kafka-cluster --body '{"kafkaClusterName":"my-kafka",...}'

# Create a resource (body from file)
scpcli apache-kafka create-kafka-cluster --body @request.json

# Save response to file
scpcli kubernetes-engine list-kubernetes-engines-v2 --output-file engines.json
```

## Building

```bash
mise run build
```

Or directly:

```bash
go build -o bin/scpcli ./cmd/scpcli
```

## Contributing

### Prerequisites

- [mise-en-place](https://mise.jdx.dev) — manages Go, golangci-lint, hk, and task running for this repo
- A Samsung SDS Cloud Platform account (required for scraping)

See the [mise installation docs](https://mise.jdx.dev/installing-mise.html) to get set up, then run `mise install` in the repo root to install all required tools.

### Git hooks

This repo uses [hk](https://hk.jdx.dev) to manage git hooks. Running `mise install` installs hk and registers the hooks automatically. To register them manually after the fact:

```bash
hk install
```

The pre-commit hook runs golangci-lint and checks for trailing whitespace, line endings, and accidentally committed private keys.

### Project structure

```
cmd/scpcli/         — CLI entry point
internal/
  auth/             — HMAC-SHA256 request signing
  client/           — HTTP client
  commands/         — Kong root command, helpers, and generated service files
  config/           — Config loading (~/.scp/ files + env vars)
tools/
  scraper/          — Fetches API docs from the SCP portal and produces openapi.yaml
  codegen/          — Reads openapi.yaml and generates internal/commands/*_gen.go
openapi.yaml        — Generated OpenAPI 3.0 spec
```

### Development tasks

| Command             | Description                                         |
| ------------------- | --------------------------------------------------- |
| `mise run build`    | Build `./bin/scpcli`                                |
| `mise run test`     | Run all tests                                       |
| `mise run lint`     | Run golangci-lint across all packages               |
| `mise run tidy`     | Tidy `go.mod` / `go.sum`                            |
| `mise run scrape`   | Fetch latest API docs and regenerate `openapi.yaml` |
| `mise run codegen`  | Regenerate commands from the current `openapi.yaml` |
| `mise run generate` | Full pipeline: scrape then codegen                  |

### Regenerating the CLI

The generated files in `internal/commands/` are committed to the repo, so you only need to run the pipeline if the upstream API changes or you are updating the generators.

#### Step 1 — Obtain session credentials

The scraper authenticates against the SCP portal using session cookies from a live browser session. Log into [cloud.samsungsds.com/console](https://cloud.samsungsds.com/console), open DevTools, go to the Network tab, click any request, and select the Cookies tab. You need two cookies:

| Cookie       | Environment variable |
| ------------ | -------------------- |
| `JSESSIONID` | `SCP_SESSION`        |
| `SESSION`    | `SCP_SPRING_SESSION` |

Copy `.env.example` to `.env` and fill in the session cookie values. `mise` loads `.env` automatically. If you already have `~/.scp/config.json` and `~/.scp/credentials.json` set up, leave the CLI config and credential variables commented out — only the session cookies are needed for scraping.

#### Step 2 — Run the pipeline

```bash
mise run generate
```

This runs the scraper (fetches docs into a temporary directory and writes `openapi.yaml`) then codegen (reads `openapi.yaml` and writes `internal/commands/*_gen.go`). Commit both outputs.

To regenerate commands from an already-updated `openapi.yaml` without re-scraping:

```bash
mise run codegen
```
