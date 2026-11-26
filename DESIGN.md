# DESIGN: vendo

## 1. Problem and Goal

Provide a small Go CLI (`vendo`, module `github.com/wolfeidau/vendo`) that mints short-lived, scoped GitHub App installation access tokens using an RSA private key stored in 1Password. The primary workflow is allowing an AI agent (or local script) to interact with `gh` CLI under a dedicated GitHub App bot identity (`<app-slug>[bot]`) so it can review and approve pull requests authored by your personal GitHub account without using your personal token.

### Non-goals (v1)

- Creating, configuring, or installing GitHub Apps on repositories.
- Caching installation tokens on disk.
- Running a long-lived background daemon or credential broker (noted as a future hardening option in Section 3).
- GitHub Enterprise Server `GH_ENTERPRISE_TOKEN` / `GH_HOST` environment injection in `exec` (v1 injects `GH_TOKEN` only; `--api-url` still supports GHES and local test servers).

## 2. Feasibility and GitHub App Mechanics

Using a GitHub App for PR reviews and approvals is a sound pattern with a few platform behaviors to account for:

- GitHub Apps authenticate as `<app-slug>[bot]`, which is a distinct actor from your personal account. The app can comment on, request changes on, and approve PRs that you opened.
- Approvals from a GitHub App with `pull_requests: write` count toward standard "Require approvals" branch protection rules. However, apps cannot be named directly in `CODEOWNERS`, so repositories requiring Code Owner review will still need a human Code Owner unless rulesets bypass that requirement.
- GitHub Apps use an RSA private key in PEM format (PKCS#1 or PKCS#8), not an X.509 certificate. In 1Password, store the downloaded `.pem` as a concealed text field or document attachment on an API Credential or Secure Note item so `op read` returns the raw PEM cleanly. Avoid 1Password's SSH Key item type for GitHub App PEM files.
- RSA private keys do not embed GitHub App permissions; granted permissions live on the GitHub App installation and can change without rotating the key. `vendo init` records the installation's granted permissions into the local config profile so agents can inspect them offline via `vendo perms` and validate requests upfront.
- GitHub App JWTs are signed with `RS256` and valid for at most 10 minutes. The tool will issue JWTs with `iat = now - 60s` (clock skew buffer) and `exp = now + 5m`.
- GitHub installation access tokens always expire after 1 hour; the API does not allow requesting a shorter TTL. To shrink the effective lifetime to seconds, the `exec` subcommand will revoke the token via `DELETE /installation/token` immediately after the child command exits.
- Installation access tokens can be downscoped per request to a subset of repositories and permissions granted to the installation. GitHub rejects any request asking for a repository or permission the installation does not already hold.

## 3. Security Model and Tradeoffs

### Threat model and blast radius

If an agent can invoke `op read` directly, it can access the long-lived RSA private key and mint tokens up to the maximum permissions granted to the GitHub App installation. Two controls bound that risk without requiring a daemon:

1. Restrict the GitHub App installation itself to only the target repositories and minimum permissions (for example `pull_requests: write`, `contents: read`, `metadata: read`). Omit `contents: write`, `administration`, and `workflows` unless explicitly required.
2. Control `op` access via 1Password Desktop biometric approval (human-in-the-loop per invocation) or a 1Password Service Account (`OP_SERVICE_ACCOUNT_TOKEN`) restricted to a dedicated vault containing only this GitHub App key.

If stronger isolation is needed later (where the agent never touches `op` or the private key), the same `internal/jwt` and `internal/github` packages can back a local broker outside the agent sandbox.

### Secret handling rules

- Never write private keys, JWTs, or installation tokens to disk. The config file stores only non-secret configuration (`app_id`, `installation_id`, `api_url`, `key_ref` or `key_file` path, recorded `permissions`, and `presets`) with mode `0600`.
- Never accept installation tokens as CLI flags (which appear in `ps`); read tokens from stdin or `GH_TOKEN` in `revoke`.
- Write all `slog` logs to `os.Stderr` and keep `os.Stdout` strictly for tokens, structured command output, or child process output.
- Never log private keys, JWTs, installation tokens, or raw `op://` references.
- Capture and redact `op` stderr on failure so secret references are not echoed in error output.

## 4. Configuration

`vendo` supports a YAML config file for named profiles so users can manage one or more GitHub Apps without repeating flags on every command.

### Location and precedence

- Default path: `$XDG_CONFIG_HOME/vendo/config.yaml`, falling back to `~/.config/vendo/config.yaml`.
- Override path via `--config` or `VENDO_CONFIG`. If explicitly set and the file does not exist, fail immediately (except for `vendo init`, which creates it). If unset and the default file does not exist, proceed using flags and environment variables.
- Profile selection: `--profile` / `VENDO_PROFILE`, falling back to `default_profile` in the config file. If `--profile` is explicitly requested and missing, fail immediately.
- Resolution precedence (highest to lowest): CLI flags, `VENDO_*` environment variables, selected config profile, built-in defaults.
- Key source precedence: `key_ref` and `key_file` are mutually exclusive within a single layer (CLI flags, env vars, or a single config profile), while a higher-precedence layer specifying either key source cleanly replaces a lower layer's key source.

### Schema

```yaml
default_profile: reviewer
profiles:
  reviewer:
    app_id: "<app-id-or-client-id>"
    key_ref: "op://<vault>/<item>/<field>"
    installation_id: 123456
    api_url: "https://api.github.com"
    permissions:
      contents: read
      metadata: read
      pull_requests: write
    presets:
      pr-review:
        pull_requests: write
        contents: read
        metadata: read
```

`permissions` records the installation's granted permissions (populated by `vendo init` or edited manually). When populated, `exec` and `token` validate requested permissions against this map upfront before invoking `op` or calling GitHub. `presets` defines optional profile-specific presets that extend or override built-in presets.

## 5. CLI Design

The CLI uses `github.com/alecthomas/kong` for flag and subcommand parsing, binding `Globals{Logger *slog.Logger}` via `kong.Bind` and `context.Context` via `kong.BindTo`.

### Global flags

| Flag | Env var | Default | Description |
| :--- | :--- | :--- | :--- |
| `--version` | | | Print version (`version (commit, date)`) and exit. |
| `--config` | `VENDO_CONFIG` | `~/.config/vendo/config.yaml` | Path to YAML config file. |
| `--profile` | `VENDO_PROFILE` | `default_profile` | Config profile name. |
| `--log-level` | `VENDO_LOG_LEVEL` | `warn` | `debug`, `info`, `warn`, or `error`. |
| `--log-format` | `VENDO_LOG_FORMAT` | `text` | `text` or `json` (`slog` handler on stderr). |
| `--app-id` | `VENDO_APP_ID` | | GitHub App ID or Client ID used as JWT `iss`. |
| `--key-ref` | `VENDO_KEY_REF` | | 1Password reference (`op://<vault>/<item>/<field>` or with section). |
| `--key-file` | `VENDO_KEY_FILE` | | Local PEM file path or `-` for stdin (mutually exclusive with `--key-ref`). |
| `--installation-id` | `VENDO_INSTALLATION_ID` | | Numeric installation ID; auto-discovered from `--repo` or git remote if omitted. |
| `--api-url` | `VENDO_API_URL` | `https://api.github.com` | GitHub REST API base URL (supports GHES and test servers). |
| `--timeout` | `VENDO_TIMEOUT` | `15s` | Timeout for `op` and GitHub API calls. |

### Built-in permission presets

Every token request (`exec` and `token`) must specify explicit permissions via `--perm`, `--preset`, or both. There is no `--all-perms` flag; omitting permissions is rejected upfront.

- `pr-review`: `pull_requests=write,contents=read,metadata=read`
- `pr-comment`: `issues=write,pull_requests=read,metadata=read` (allows top-level PR comments via `gh pr comment` without granting PR approval rights)
- `read-only`: `contents=read,pull_requests=read,issues=read,metadata=read`

When `--preset` is combined with `--perm` for the same permission name, the explicit `--perm` value overrides the preset.

### Subcommands

#### `exec` (recommended for agents)

Mints a scoped installation token, executes a child command with `GH_TOKEN` injected into its environment, and revokes the token as soon as the child exits.

```sh
vendo exec --preset pr-review -- gh pr review 42 --approve -b "LGTM"
```

Flags:
- `-r, --repo` (repeatable): Target `owner/repo`. Defaults to the current git repository's `origin` remote when neither `--repo` nor `--all-repos` is provided.
- `--all-repos`: Explicitly opt into all repositories accessible to the installation. Requires `--installation-id` (via flag, env var, or config profile).
- `-p, --perm` (repeatable): Permission pair `name=level` (e.g., `pull_requests=write`, `contents=read`).
- `--preset`: Named permission preset (built-in or defined on the active config profile). Can be combined with extra `--perm` flags.
- `--no-revoke`: Skip calling `DELETE /installation/token` when the child exits.
- `<cmd> [args...]`: Command and arguments to run (passed after `--`).

Behavior:
- Rejects `--key-file -` (stdin) to avoid stdin contention with the child process.
- Sets `GH_TOKEN=<token>` in the child environment and unsets `GITHUB_TOKEN` so `gh` unambiguously authenticates as the app rather than any ambient personal token.
- Forwards `SIGINT` and `SIGTERM` to the child process, runs best-effort token revocation using a detached bounded context (`context.WithTimeout(context.Background(), 3*time.Second)`) after the child terminates, and exits with the child's exit code (or `128 + signal` when terminated by a signal).

#### `token`

Mints a scoped installation token and prints it to stdout for shell pipelines (`GH_TOKEN=$(vendo token ...)`).

```sh
GH_TOKEN=$(vendo token --preset pr-review) gh pr review 42 --approve
```

Flags:
- Same scoping flags as `exec` (`--repo`, `--all-repos`, `--perm`, `--preset`).
- `--format`: `raw` (default, prints only the token followed by a newline) or `json` (prints `token`, `expires_at`, `permissions`, `repositories`).

#### `perms`

Lists the permissions available to the active profile (and configured presets) so users and agents can see what can be requested without trial and error.

```sh
vendo perms
vendo perms --live -r owner/repo
```

Flags:
- `--live`: Authenticate with the App key and query GitHub (`GET /app/installations/{id}` or `GET /repos/{owner}/{repo}/installation`) instead of reading recorded permissions from config.
- `-r, --repo`: Target `owner/repo` used for installation lookup when `--live` is set and `--installation-id` is omitted.
- `--format`: `text` (default) or `json`.

Behavior:
- Without `--live`, reads `permissions` and `presets` from the active config profile locally without invoking `op` or GitHub. If no permissions are recorded in config, returns an actionable error suggesting `vendo init` or `vendo perms --live`.

#### `init`

Bootstraps or refreshes a config profile by validating credentials against GitHub and recording the installation ID and granted permissions in `config.yaml`.

```sh
vendo init --profile reviewer --app-id <id> --key-ref "op://<vault>/<item>/<field>" -r owner/repo --set-default
```

Flags:
- `-r, --repo`: Target `owner/repo` for discovering the installation ID when `--installation-id` is omitted (defaults to current git `origin` remote).
- `--set-default`: Set this profile as `default_profile` (automatic when creating a new config file).
- `--force`: Overwrite an existing profile with the same name (also used to re-sync recorded permissions after changing GitHub App settings).

Behavior:
- Validates key, calls `GET /app`, resolves the installation via `--installation-id` or `GET /repos/{owner}/{repo}/installation`, and writes the profile (including `installation_id` and `permissions`) atomically to `config.yaml` with `0700` directory / `0600` file permissions.

#### `check`

Validates configuration upfront without minting or printing a token:

1. Loads and parses the RSA private key from `--key-ref` or `--key-file` (resolved via flags, env, or config profile).
2. Signs a short-lived App JWT and calls `GET /app`.
3. Resolves the installation via `--installation-id` (`GET /app/installations/{id}`) or `-r, --repo` / git remote (`GET /repos/{owner}/{repo}/installation`).
4. Prints the App slug, App ID, installation ID, and live granted installation permissions to stdout, noting any drift against permissions recorded in the config profile.

Flags:
- `-r, --repo`: Target `owner/repo` for installation lookup when `--installation-id` is omitted.

#### `revoke`

Revokes an installation token via `DELETE /installation/token`.

Flags:
- `--token-stdin`: Read the token from stdin (takes precedence over `GH_TOKEN`).

Behavior:
- Without `--token-stdin`, reads `GH_TOKEN` from the environment. Rejects empty input immediately rather than blocking on stdin.

## 6. Architecture and Package Layout

```text
cmd/vendo/
  main.go            # entrypoint, signal handling, Kong parse, version info
  main_test.go       # real-binary integration tests using TestMain + httptest
internal/
  commands/          # Kong CLI grammar, Globals, flag validation, subcommand Run methods
  config/            # YAML config loading, profile resolution, atomic save, permission cap checks
  github/            # Minimal net/http client for GitHub App REST endpoints
  jwt/               # Stdlib RS256 JWT signer supporting PKCS#1 and PKCS#8 PEM keys
  keysource/         # Private key loaders: 1Password (`op read`), file, stdin
  gitrepo/           # Resolve and parse owner/repo via `git remote get-url origin`
```

### Dependencies

Keep runtime dependencies minimal:
- `github.com/alecthomas/kong` for CLI parsing.
- `go.yaml.in/yaml/v3` for YAML config file reading and writing.
- Go standard library for RSA signing (`crypto/rsa`, `crypto/x509`, `crypto/sha256`, `encoding/pem`), HTTP (`net/http`), and structured logging (`log/slog`). No third-party JWT library or GitHub SDK is needed for five REST endpoints.
- `github.com/stretchr/testify/require` for tests only.

### GitHub API endpoints

All requests include `Accept: application/vnd.github+json`, `X-GitHub-Api-Version: 2022-11-28`, and `User-Agent: vendo/<version>`.

| Operation | Endpoint | Auth | Notes |
| :--- | :--- | :--- | :--- |
| Verify app | `GET /app` | App JWT | Returns app `id`, `slug`, `name`. |
| Fetch installation | `GET /app/installations/{installation_id}` | App JWT | Used by `check`, `init`, and `perms --live` when `--installation-id` is known. |
| Discover installation | `GET /repos/{owner}/{repo}/installation` | App JWT | Used when `--installation-id` is omitted; validates all `--repo` targets share one owner (case-insensitive). |
| Mint token | `POST /app/installations/{id}/access_tokens` | App JWT | Request body `repositories` takes bare repo names (without `owner/` prefix); `permissions` takes `map[string]string`. |
| Revoke token | `DELETE /installation/token` | Installation token | Expects `204 No Content`. |

### Key retrieval (`internal/keysource`)

- `op` source invokes `op read --no-newline <ref>` via `exec.CommandContext` (no shell interpolation).
- If `op` fails, the returned error reports the exit status and a sanitized message without echoing raw stderr or the `op://` reference.
- File source reads the PEM file directly (or stdin for `-`, disallowed in `exec`).
- Both paths pass bytes into `jwt.ParsePrivateKey`, which accepts both `RSA PRIVATE KEY` (PKCS#1) and `PRIVATE KEY` (PKCS#8) PEM blocks and rejects non-RSA keys upfront.

### Git remote detection (`internal/gitrepo`)

- Invokes `git remote get-url origin` via `exec.CommandContext` so subdirectories, git worktrees, submodules, and `url.<base>.insteadOf` rewrites work automatically.
- Parses SSH (`git@github.com:owner/repo.git`, `ssh://git@github.com[:port]/owner/repo.git`) and HTTPS (`https://github.com/owner/repo[.git]`) URLs into `owner/repo`.

## 7. Validation and Error Handling

Validate everything locally before invoking `op` or calling GitHub:
- Validate config file syntax, profile existence, and non-conflicting key source fields within each precedence layer.
- Require `--app-id` and exactly one of `--key-ref` or `--key-file` (after applying config/env/flag precedence) for commands that sign JWTs (`exec`, `token`, `init`, `check`, `perms --live`).
- Validate `--key-ref` starts with `op://` and contains 3 or 4 non-empty path segments (`op://vault/item/field` or `op://vault/item/section/field`, with optional `?` query parameters).
- Validate `--repo` entries match `owner/repo`, verify all `--repo` targets share one owner (case-insensitive), reject mixing `--repo` with `--all-repos`, and require `--installation-id` when `--all-repos` is set.
- Validate `--perm` entries match `<name>=read|write|admin`, reject any `exec` or `token` invocation without at least one `--perm` or `--preset`, and reject unknown preset names.
- When the active config profile records `permissions`, verify every requested permission exists in the recorded map and does not exceed its recorded level (`read < write < admin`).
- Reject `--key-file -` on `exec`.
- Translate common GitHub API errors into actionable messages: `401` (App ID/key mismatch, clock skew, or invalid/expired token on `revoke`), `404` (app not installed on the target repository or unknown installation ID), `422` (requested permission or repository exceeds installation grant).

## 8. Tooling and Testing Plan

### Tooling

Configured via `mise.toml` (pinned tool versions), `.golangci.yml` (enforcing `forbidigo` against `slog.Default()` and `depguard` against `testify/assert`), and `lefthook.yml` for pre-commit `check:tidy` and `check:lint`.

### Tests

- `internal/jwt`: RS256 signing and verification against ephemeral PKCS#1 and PKCS#8 keys, clock skew bounds, invalid PEM rejection.
- `internal/keysource`: file and stdin loading, `op` invocation against a fake `op` script in a temp `PATH`, 3- and 4-segment `op://` validation, timeout cancellation, stderr redaction on failure.
- `internal/config`: YAML parsing, XDG path resolution, profile selection, CLI/env/config precedence, key source replacement across layers, atomic save with `0600` permissions, recorded permission cap validation.
- `internal/gitrepo`: parsing SSH, `ssh://`, and HTTPS remote URLs (including custom SSH host aliases and `.git` suffixes), plus real temp git repo detection and non-git directory handling.
- `internal/github`: `httptest.Server` tests verifying headers, bare repository name stripping in `access_tokens`, permission map serialization, installation lookup by repo and by ID, revocation, and HTTP error mapping (`401`, `404`, `422`).
- `cmd/vendo`: single binary built in `TestMain` with isolated `HOME`, fake `op` in `PATH`, and `httptest.Server` via `--api-url`. Covers `init`, `perms` (offline and `--live`, `text` and `json`), `check` (including drift warning), `token` (`raw` and `json`), `exec` (verifying `GH_TOKEN` injection, `GITHUB_TOKEN` unset, exit code propagation, signal forwarding + detached revocation, and `--key-file -` rejection), `revoke` (`GH_TOKEN` and `--token-stdin` precedence), and upfront validation failures.

## 9. Implementation Plan

1. Add Go module (`github.com/wolfeidau/vendo`) and repository scaffolding (`go.mod`, `.golangci.yml`, `lefthook.yml`, `.goreleaser.yaml`).
2. Implement `internal/jwt`, `internal/keysource`, `internal/gitrepo`, `internal/config`, and `internal/github` with unit tests.
3. Implement `internal/commands` and `cmd/vendo` with Kong wiring, `slog` setup, and real-binary tests.
4. Run full verification (`go vet`, `go test -race -count=1`, `golangci-lint`, `mise run check:tidy`, race-built binary tests).
5. Document GitHub App creation, 1Password storage, config setup (`vendo init`), and `gh` usage in `README.md`, then verify end-to-end against a real GitHub App and PR.
