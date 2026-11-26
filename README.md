# vendo

`vendo` is a Go CLI that mints short-lived, downscoped GitHub App installation access tokens from an RSA private key stored in 1Password (or a local PEM file).

It lets AI agents and local scripts run `gh` as a dedicated GitHub App bot (`<app-slug>[bot]`) instead of using your personal GitHub token, and revokes the token as soon as the command exits.

```sh
vendo exec --preset pr-review -- gh pr review 42 --approve -b "LGTM"
```

## Install

When using `--key-ref`, the 1Password CLI (`op`) must be on `PATH`.

Install a prebuilt release binary (macOS arm64/amd64, Linux amd64/arm64) with [install.sh](install.sh). The script verifies SHA-256 checksums, plus GitHub Release attestations when `gh` is available and keyless SLSA v1 provenance when `cosign` is available:

```sh
curl -fsSL https://raw.githubusercontent.com/wolfeidau/vendo/main/install.sh | sh
```

Or install a tagged release with Go 1.27+:

```sh
go install github.com/wolfeidau/vendo/cmd/vendo@<version>
```

<details>
<summary>Pin a release, build from source, or verify release assets manually</summary>

Pin a specific version or install prefix:

```sh
curl -fsSL https://raw.githubusercontent.com/wolfeidau/vendo/main/install.sh | sh -s -- --version v0.1.0 --prefix ~/.local
```

Build from source:

```sh
git clone https://github.com/wolfeidau/vendo.git
cd vendo
go build -o vendo ./cmd/vendo
```

Verify a downloaded release archive manually (each release publishes `.tar.gz`, `.sha256`, `.sbom.json`, and `.sigstore.json` signed by `https://buildkite.com/wolfeidauci/vendo-release`):

```sh
# 1. Verify SHA-256 checksum (use shasum -a 256 on macOS)
diff -u <(sha256sum vendo_linux_amd64.tar.gz | cut -d' ' -f1) vendo_linux_amd64.tar.gz.sha256

# 2. Verify immutable GitHub Release asset attestation
gh release verify-asset v0.1.0 vendo_linux_amd64.tar.gz --repo wolfeidau/vendo

# 3. Verify keyless SLSA v1 provenance via Sigstore/Cosign
cosign verify-blob-attestation \
  --bundle vendo_linux_amd64.tar.gz.sigstore.json \
  --type slsaprovenance1 \
  --certificate-oidc-issuer https://agent.buildkite.com \
  --certificate-identity https://buildkite.com/wolfeidauci/vendo-release \
  vendo_linux_amd64.tar.gz
```

</details>

## Quickstart

1. Create a GitHub App (Settings, Developer settings, GitHub Apps, New GitHub App) with webhook disabled, install it on only the target repositories, and grant minimal permissions (for PR review agents: `pull_requests: write`, `contents: read`, `metadata: read`, plus `issues: write` if posting top-level PR comments).
2. Generate an RSA private key (`.pem`), save the raw PEM text in 1Password as a Concealed field or Document attachment on an API Credential or Secure Note item (do not use the SSH Key item type), and delete the local `.pem` file.
3. Initialize a default profile in `~/.config/vendo/config.yaml`:

```sh
vendo init \
  --profile reviewer \
  --app-id <app-id> \
  --key-ref "op://<vault>/<item>/<field>" \
  -r owner/repo \
  --set-default
```

4. Run `gh` with a downscoped token that is automatically revoked when `gh` exits:

```sh
vendo exec --preset pr-review -- gh pr review 42 --approve -b "LGTM"
```

## Examples

Approve a pull request in the current git repository (repo is detected from `git remote get-url origin`):

```sh
vendo exec --preset pr-review -- gh pr review 42 --approve -b "LGTM"
```

Post a top-level PR comment without granting approval rights:

```sh
vendo exec -r owner/repo --preset pr-comment -- gh pr comment 42 -b "Tests pass."
```

Combine a preset with an explicit `-p, --perm` override (explicit `--perm` values override the preset):

```sh
vendo exec --preset read-only -p pull_requests=write -- gh pr review 42 --comment -b "Reviewed"
```

Inspect permissions and presets recorded in the active profile offline (no `op` or GitHub call):

```sh
vendo perms
```

Verify credentials and detect permission drift against GitHub without minting a token:

```sh
vendo check
```

Mint a token for a shell pipeline and revoke it explicitly afterwards:

```sh
TOKEN=$(vendo token --preset read-only)
GH_TOKEN="$TOKEN" gh pr list
printf '%s' "$TOKEN" | vendo revoke --token-stdin
```

## Permissions and presets

Every `exec` and `token` call requires explicit permissions via `--preset`, repeatable `-p, --perm <name>=read|write|admin`, or both. There is no `--all-perms` flag, and requests exceeding the profile's recorded permissions fail upfront before invoking `op` or GitHub.

| Preset | Permissions | Typical use |
| :--- | :--- | :--- |
| `pr-review` | `pull_requests=write`, `contents=read`, `metadata=read` | Reviewing, requesting changes on, and approving PRs |
| `pr-comment` | `issues=write`, `pull_requests=read`, `metadata=read` | Top-level PR comments via `gh pr comment` |
| `read-only` | `contents=read`, `pull_requests=read`, `issues=read`, `metadata=read` | Reading repository code, PRs, and issues |

You can add or override presets per profile under `presets` in `config.yaml`.

## Commands

| Command | Description |
| :--- | :--- |
| `vendo exec` | Mint a scoped token, run a command with `GH_TOKEN` set (`GITHUB_TOKEN` unset), and revoke on exit |
| `vendo token` | Mint a scoped token and print `raw` (default) or `json` to stdout |
| `vendo perms` | List recorded permissions and presets offline, or query live with `--live` |
| `vendo init` | Validate credentials and save or refresh (`--force`) a config profile |
| `vendo check` | Validate credentials, installation lookup, and permission drift without minting a token |
| `vendo revoke` | Revoke an installation token from `GH_TOKEN` or `--token-stdin` |

Run `vendo --help` or `vendo <command> --help` for the full flag list and `VENDO_*` environment variables.

## Configuration

`vendo` reads `$XDG_CONFIG_HOME/vendo/config.yaml` (falling back to `~/.config/vendo/config.yaml`), with precedence CLI flags > `VENDO_*` env vars > selected profile > defaults:

```yaml
default_profile: reviewer
profiles:
  reviewer:
    app_id: "<app-id>"
    key_ref: "op://<vault>/<item>/<field>"
    installation_id: 123456
    permissions:
      contents: read
      metadata: read
      pull_requests: write
    presets:
      triage:
        issues: write
        metadata: read
```

Use `key_file` instead of `key_ref` to load a local PEM file. Use `--api-url` (`VENDO_API_URL` or `api_url`) to target GitHub Enterprise Server or a local test server.

## Good to know

- Approvals from a GitHub App with `pull_requests: write` count toward standard "Require approvals" branch protection rules, but GitHub Apps cannot be listed in `CODEOWNERS`.
- GitHub installation tokens always expire after 1 hour; `vendo exec` shortens effective exposure to the child command's lifetime by calling `DELETE /installation/token` on exit.
- Keep keys tightly scoped: restrict the GitHub App installation to selected repositories and minimum permissions, and gate `op` with 1Password Desktop biometric approval (interactive) or a Service Account scoped to a single dedicated vault (unattended).
- See [DESIGN.md](DESIGN.md) for architecture, full configuration precedence, and threat model details.

## Built with AI

This project is built with AI-assisted development workflows using tools including
[OpenAI Codex](https://developers.openai.com/codex/), [Claude Code](https://claude.ai/code), and
[Gemini](https://gemini.google.com).

## License

Apache-2.0. See [LICENSE](LICENSE).
