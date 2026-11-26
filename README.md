# vendo

`vendo` is a Go CLI (`github.com/wolfeidau/vendo`) that mints short-lived, downscoped GitHub App installation access tokens using an RSA private key stored in 1Password (or a local PEM file).

It is designed for AI agents and local scripts that run `gh` under a dedicated GitHub App bot identity (`<app-slug>[bot]`) rather than a personal GitHub token.

## How it fits agent and `gh` workflows

Authenticating as `<app-slug>[bot]` gives an agent a separate actor identity from your personal GitHub account. The app can comment on, request changes on, and approve pull requests that you opened.

Two GitHub platform constraints shape how `vendo` works:

- Approvals from a GitHub App with `pull_requests: write` count toward standard "Require approvals" branch protection rules, but GitHub Apps cannot be listed in `CODEOWNERS`. Repositories that require Code Owner review still need a human Code Owner unless repository rulesets bypass that requirement.
- GitHub installation access tokens always expire after 1 hour; the API does not accept a shorter TTL. To reduce exposure to the duration of a single command, `vendo exec` injects `GH_TOKEN`, unsets `GITHUB_TOKEN`, runs the child command, and immediately revokes the token via `DELETE /installation/token` when the child exits.

`vendo` injects `GH_TOKEN` only (not `GH_ENTERPRISE_TOKEN` or `GH_HOST`). Use `--api-url` to target GitHub Enterprise Server or local test servers.

## GitHub App setup

1. Open GitHub Settings, Developer settings, GitHub Apps, and select New GitHub App.
2. Disable the active webhook (not required for token minting).
3. Grant the minimum repository permissions needed for your workflow. For PR review agents, start with:
   - Pull requests: Read and write (`pull_requests: write`)
   - Contents: Read-only (`contents: read`)
   - Metadata: Read-only (`metadata: read`, granted automatically)
   - Issues: Read and write (`issues: write`) only if the agent posts top-level PR comments with `pr-comment`
   - Leave `contents: write`, `administration`, and `workflows` unassigned unless explicitly required.
4. Create the app and record the App ID (or Client ID; either works as the JWT `iss` claim).
5. Generate and download a private key (`.pem`, PKCS#1 or PKCS#8 RSA).
6. Install the app on only the target repositories ("Only select repositories") rather than all repositories.

## Storing the private key in 1Password

GitHub Apps issue an RSA private key in PEM format (`RSA PRIVATE KEY` or `PRIVATE KEY`), not an X.509 certificate.

- Store the `.pem` contents as a Concealed text field or Document attachment on an API Credential or Secure Note item so `op read --no-newline "op://<vault>/<item>/<field>"` returns the raw PEM text cleanly.
- Do not use the 1Password SSH Key item type for GitHub App PEM files.
- Delete the downloaded `.pem` file from local disk once it is saved in 1Password.
- Reference keys using `op://<vault>/<item>/<field>` or `op://<vault>/<item>/<section>/<field>` (optional `?` query parameters are supported).

Control access to `op` based on how the agent runs:

- For interactive local use, enable 1Password Desktop CLI integration with biometric approval so each token mint requires human approval.
- For unattended or sandboxed runs, use a 1Password Service Account (`OP_SERVICE_ACCOUNT_TOKEN`) scoped strictly to a dedicated vault that contains only this GitHub App key.

## Installation

Prerequisites: when using `--key-ref`, the 1Password CLI (`op`) must be on `PATH`.

Install a prebuilt release binary (macOS arm64/amd64, Linux amd64/arm64) using [install.sh](install.sh). The installer always checks the archive's SHA-256 checksum, verifies the immutable GitHub Release attestation when `gh` is available, and verifies the keyless SLSA v1 provenance bundle when `cosign` is available:

```sh
curl -fsSL https://raw.githubusercontent.com/wolfeidau/vendo/main/install.sh | sh
```

Pin a specific release or install prefix:

```sh
curl -fsSL https://raw.githubusercontent.com/wolfeidau/vendo/main/install.sh | sh -s -- --version v0.1.0 --prefix ~/.local
```

Or install a tagged release with Go 1.27+:

```sh
go install github.com/wolfeidau/vendo/cmd/vendo@<version>
```

Or build from source:

```sh
git clone https://github.com/wolfeidau/vendo.git
cd vendo
go build -o vendo ./cmd/vendo
```

### Verifying releases

Each release publishes archives (`vendo_<os>_<arch>.tar.gz`), per-file SHA-256 checksums (`.sha256`), SPDX JSON SBOMs (`.sbom.json` and `.sbom.json.sha256`), and keyless Sigstore SLSA v1 provenance bundles (`.sigstore.json`) signed by the tag-only Buildkite release pipeline (`https://buildkite.com/wolfeidauci/vendo-release`).

Manual verification for a downloaded release asset:

```sh
# 1. Verify SHA-256 checksum (use shasum -a 256 on macOS)
diff -u <(sha256sum vendo_linux_amd64.tar.gz | cut -d' ' -f1) vendo_linux_amd64.tar.gz.sha256

# 2. Verify immutable GitHub Release asset attestation
gh release verify-asset v0.1.0 vendo_linux_amd64.tar.gz --repo wolfeidau/vendo

# 3. Verify keyless SLSA v1 build provenance via Sigstore/Cosign
cosign verify-blob-attestation \
  --bundle vendo_linux_amd64.tar.gz.sigstore.json \
  --type slsaprovenance1 \
  --certificate-oidc-issuer https://agent.buildkite.com \
  --certificate-identity https://buildkite.com/wolfeidauci/vendo-release \
  vendo_linux_amd64.tar.gz
```

## Quickstart

Initialize a profile by validating credentials against GitHub and recording the installation ID and granted permissions in `~/.config/vendo/config.yaml`:

```sh
vendo init \
  --profile reviewer \
  --app-id <app-id> \
  --key-ref "op://<vault>/<item>/<field>" \
  -r owner/repo \
  --set-default
```

Inspect the permissions recorded in the active profile offline (no `op` or GitHub call):

```sh
vendo perms
```

Verify live credentials, installation lookup, and permission drift against GitHub without minting a token:

```sh
vendo check
```

Run `gh` with a short-lived, downscoped token that is revoked automatically on exit:

```sh
vendo exec --preset pr-review -- gh pr review 42 --approve -b "LGTM"
```

## Permissions and built-in presets

Every token request (`exec` and `token`) must declare explicit permissions with `--perm`, `--preset`, or both. There is no `--all-perms` flag, and omitting permissions fails upfront before calling `op` or GitHub. Requiring explicit permissions keeps each token scoped to the single action being run and prevents accidental broad grants.

Built-in presets:

| Preset | Permissions | Intended use |
| :--- | :--- | :--- |
| `pr-review` | `pull_requests=write`, `contents=read`, `metadata=read` | Reviewing, requesting changes on, and approving pull requests |
| `pr-comment` | `issues=write`, `pull_requests=read`, `metadata=read` | Top-level PR comments via `gh pr comment` without approval rights |
| `read-only` | `contents=read`, `pull_requests=read`, `issues=read`, `metadata=read` | Reading repository code, PRs, and issues |

Permission resolution rules:

- Specify individual permissions with repeatable `-p, --perm <name>=read|write|admin`.
- Combine `--preset` with `--perm` to add or adjust permissions; when both specify the same permission name, the explicit `--perm` value overrides the preset.
- Define additional or overridden presets per profile under `presets` in `config.yaml`.
- When the active profile includes a recorded `permissions` map (populated by `vendo init`), `vendo` checks every requested permission against that map upfront (`read < write < admin`) before calling `op` or GitHub.

## Subcommands

### `init`

Bootstraps or refreshes a config profile by loading the private key, calling `GET /app`, resolving the installation via `--installation-id` or `-r, --repo` (defaulting to the current git `origin` remote), and writing the profile atomically to `config.yaml` (`0700` directory, `0600` file).

```sh
vendo init --profile reviewer --app-id <app-id> --key-ref "op://<vault>/<item>/<field>" -r owner/repo --set-default
vendo init --profile reviewer --force
```

Flags:

- `-r, --repo`: target `owner/repo` used to discover the installation ID when `--installation-id` is omitted (defaults to current git `origin` remote).
- `--set-default`: set this profile as `default_profile` (automatic when creating a new config file).
- `--force`: overwrite an existing profile with the same name, or re-sync recorded permissions after changing GitHub App settings.

### `perms`

Lists permissions and presets available to the active profile so users and agents can check what can be requested without trial and error.

```sh
vendo perms
vendo perms --format json
vendo perms --live -r owner/repo
```

Flags:

- `--live`: authenticate with the app key and query GitHub (`GET /app/installations/{id}` or `GET /repos/{owner}/{repo}/installation`) instead of reading recorded permissions from `config.yaml`.
- `-r, --repo`: target `owner/repo` used for installation lookup when `--live` is set and `--installation-id` is omitted.
- `--format`: `text` (default) or `json`.

Without `--live`, `perms` reads `permissions` and `presets` locally without invoking `op` or GitHub. If the profile has no recorded permissions, it returns an error pointing to `vendo init` or `vendo perms --live`.

### `check`

Validates configuration and credentials against GitHub without minting or printing a token. It loads and parses the RSA private key, calls `GET /app`, resolves the installation, and prints the App slug, App ID, installation ID, and live granted permissions, noting any drift from the profile's recorded permissions.

```sh
vendo check
vendo check -r owner/repo
```

Flags:

- `-r, --repo`: target `owner/repo` for installation lookup when `--installation-id` is omitted.

### `exec`

Mints a scoped installation token, executes a child command with `GH_TOKEN` set and `GITHUB_TOKEN` unset, and revokes the token as soon as the child exits. This is the recommended subcommand for AI agents and scripts.

```sh
vendo exec --preset pr-review -- gh pr review 42 --approve -b "LGTM"
vendo exec -r owner/repo --preset pr-comment -- gh pr comment 42 -b "Tests pass."
vendo exec --preset read-only -p pull_requests=write -- gh pr review 42 --comment -b "Reviewed"
vendo exec --all-repos --installation-id <installation-id> --preset read-only -- gh api /installation/repositories
```

Flags:

- `-r, --repo` (repeatable): target `owner/repo`. Defaults to the current git repository's `origin` remote when neither `--repo` nor `--all-repos` is set. Multiple `--repo` values must share a single owner (case-insensitive).
- `--all-repos`: explicitly request access to all repositories on the installation. Cannot be combined with `--repo`, and requires `--installation-id` (via flag, env var, or config profile).
- `-p, --perm` (repeatable): permission pair `name=level` (`read`, `write`, or `admin`).
- `--preset`: built-in or profile-defined preset name.
- `--no-revoke`: skip calling `DELETE /installation/token` after the child exits.
- `<cmd> [args...]`: command and arguments to run, passed after `--`.

Execution behavior:

- Rejects `--key-file -` (stdin) to avoid stdin contention with the child process.
- Sets `GH_TOKEN` and unsets `GITHUB_TOKEN` in the child environment so `gh` authenticates unambiguously as the GitHub App rather than any ambient personal token.
- Forwards `SIGINT` and `SIGTERM` to the child process, runs best-effort token revocation with a detached 3-second timeout after the child exits, and returns the child's exit code (or `128 + signal` when terminated by a signal).

### `token`

Mints a scoped installation token and writes it to stdout for shell pipelines. Revoke manually with `vendo revoke` when finished if the token should not remain valid for the full 1-hour GitHub TTL.

```sh
GH_TOKEN=$(vendo token --preset pr-review) gh pr review 42 --approve
vendo token --preset read-only --format json
```

Flags:

- `-r, --repo`, `--all-repos`, `-p, --perm`, `--preset`: same scoping flags as `exec`.
- `--format`: `raw` (default, prints only the token followed by a newline) or `json` (prints `token`, `expires_at`, `permissions`, and `repositories`).

### `revoke`

Revokes an installation token via `DELETE /installation/token`. Does not require `--app-id` or a private key source.

```sh
vendo revoke
printf '%s' "$TOKEN" | vendo revoke --token-stdin
```

Flags:

- `--token-stdin`: read the token from stdin (takes precedence over `GH_TOKEN`).

Without `--token-stdin`, `revoke` reads `GH_TOKEN` from the environment and fails immediately if empty rather than blocking on stdin. Tokens are never accepted as CLI flags.

## Configuration

### File location and precedence

- Default config path: `$XDG_CONFIG_HOME/vendo/config.yaml`, falling back to `~/.config/vendo/config.yaml`.
- Override the path with `--config` or `VENDO_CONFIG`. If set explicitly and the file is missing, `vendo` fails immediately (except `vendo init`, which creates it). If unset and the default file does not exist, `vendo` proceeds using flags and environment variables.
- Select a profile with `--profile` or `VENDO_PROFILE`, falling back to `default_profile` in `config.yaml`. Requesting a nonexistent profile explicitly fails immediately.
- Resolution precedence from highest to lowest: CLI flags, `VENDO_*` environment variables, selected config profile, built-in defaults.
- Key source precedence: `key_ref` and `key_file` are mutually exclusive within any single layer (CLI flags, environment variables, or a single profile). A higher-precedence layer specifying either key source cleanly replaces a lower layer's key source (for example, passing `--key-file` on the CLI overrides `key_ref` stored in `config.yaml`).

### Profile schema

```yaml
default_profile: reviewer
profiles:
  reviewer:
    app_id: "<app-id>"
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

Set `key_file` instead of `key_ref` if loading a local PEM file. Unknown YAML fields are rejected during config parsing.

### Global flags and environment variables

| Flag | Environment variable | Default | Description |
| :--- | :--- | :--- | :--- |
| `--version` | | | Print version (`version (commit, date)`) and exit |
| `--config` | `VENDO_CONFIG` | `~/.config/vendo/config.yaml` | Path to YAML config file (`$XDG_CONFIG_HOME/vendo/config.yaml` when `XDG_CONFIG_HOME` is set) |
| `--profile` | `VENDO_PROFILE` | `default_profile` | Config profile name |
| `--log-level` | `VENDO_LOG_LEVEL` | `warn` | Log level: `debug`, `info`, `warn`, or `error` |
| `--log-format` | `VENDO_LOG_FORMAT` | `text` | Log format on stderr: `text` or `json` |
| `--app-id` | `VENDO_APP_ID` | | GitHub App ID or Client ID used as JWT `iss` |
| `--key-ref` | `VENDO_KEY_REF` | | 1Password reference (`op://<vault>/<item>/<field>` or with section) |
| `--key-file` | `VENDO_KEY_FILE` | | Local PEM file path or `-` for stdin (mutually exclusive with `--key-ref`) |
| `--installation-id` | `VENDO_INSTALLATION_ID` | | Numeric installation ID; auto-discovered from `--repo` or git `origin` remote if omitted |
| `--api-url` | `VENDO_API_URL` | `https://api.github.com` | GitHub REST API base URL (supports GHES and local test servers) |
| `--timeout` | `VENDO_TIMEOUT` | `15s` | Timeout for `op` and GitHub API calls |

## Security model

### Threat model and blast radius

Any process that can run `op read` directly can read the long-lived RSA private key and mint tokens up to the maximum permissions granted to the GitHub App installation. Bound that blast radius with two controls:

1. Restrict the GitHub App installation itself to only the required repositories and minimum permissions.
2. Gate `op` access with 1Password Desktop biometric approval (human-in-the-loop per invocation) or a 1Password Service Account (`OP_SERVICE_ACCOUNT_TOKEN`) limited to a dedicated vault holding only this key.

### Secret handling guarantees

- Private keys, signed App JWTs (`RS256`, `iat = now - 60s`, `exp = now + 5m`), and installation tokens remain in memory only and are never written to disk. `config.yaml` stores only non-secret configuration and is written atomically with mode `0600`.
- Installation tokens are never accepted as CLI flags (which expose secrets in `ps`); `revoke` reads tokens only from `GH_TOKEN` or `--token-stdin`.
- `op read --no-newline <ref>` runs directly via `exec.CommandContext` without shell interpolation, and stderr from `op` is captured and redacted on failure so secret references are not echoed in errors.
- All `slog` output writes to `stderr`, keeping `stdout` clean for tokens, JSON output, or child process output. Private keys, JWTs, installation tokens, and raw `op://` references are never logged.
- Flags, config syntax, key source conflicts, `op://` reference syntax, repository targets, permission formats, and recorded profile permission caps are validated upfront before calling `op` or the GitHub API.

## Built with AI

This project is built with AI-assisted development workflows using tools including
[OpenAI Codex](https://developers.openai.com/codex/), [Claude Code](https://claude.ai/code), and
[Gemini](https://gemini.google.com).

## License

Apache-2.0. See [LICENSE](LICENSE).
