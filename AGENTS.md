## Public repository

This repository is public. Keep secrets and private details out of commits, code, tests, issues, and pull requests, including private keys, GitHub App IDs, Client IDs, installation IDs, 1Password references (vault, item, or field names), account details, and private paths. Use placeholders such as `op://<vault>/<item>/<field>`.

When running something that needs a credential, keep it out of command output and logs: pass it to the one command that needs it, and never print, write, or commit it. Tool errors (such as `op`) can echo a secret reference, so silence or redact their stderr.

## Verification

Fast loop for unit tests:

```sh
go test ./internal/...
```

Before finishing, after your last edit:

```sh
go vet ./... && go test -race -count=1 ./...
mise exec -- golangci-lint run ./...
mise run check:tidy
```

To race-check the compiled binary in real-binary tests:

```sh
GOFLAGS=-race GORACE=atexit_sleep_ms=0 go test -race -count=1 ./cmd/...
```

## Guiding principles

- Validate upfront. Check flags, key source options, `op://` reference syntax, repository names, and permission specs before invoking `op` or calling the GitHub API. Users and agents should see configuration errors immediately rather than after external I/O.
- Keep secrets out of disk, logs, and process arguments. Never write private keys or minted tokens to files, never pass tokens as CLI flags (where they appear in `ps`), and keep `slog` output on `os.Stderr` so `os.Stdout` remains clean for machine-readable output.

## Code conventions

### Errors

Return errors up the stack; log only at the top level (the command `Run` function or `main`). Never log an error and then return it, because it will be logged twice.

Wrap errors from other packages with context using `%w`. Errors from the same package may be returned as they are when wrapping would add nothing:

```go
return fmt.Errorf("failed to parse base URL: %w", err)
```

Do not add error handling for impossible cases. Trust the compiler and internal invariants.

### Logging

Use `slog` throughout, writing to `os.Stderr`. Pass the logger via `Globals.Logger` rather than calling `slog.Default()` in business logic. Reserve `slog.Default()` for package-level handlers where dependency injection is impractical.

Use `InfoContext` / `ErrorContext` when a context is available. Never log private keys, signed JWTs, installation tokens, or raw 1Password references.

### CLI flags

Use `github.com/alecthomas/kong` for CLI parsing and subcommands. Bind `context.Context` with `kong.BindTo(ctx, (*context.Context)(nil))` and shared dependencies with `kong.Bind(&commands.Globals{Logger: logger})`.

### Contexts

Pass `context.Context` as the first argument to every function that does I/O, makes external calls, or should respect cancellation. Do not store contexts in structs.

### Package names

Lowercase, single word, descriptive of the domain, not the layer. Examples: `commands`, `github`, `jwt`, `keysource`, `gitrepo`. Avoid generic names like `util`, `helpers`, `common`.

### Comments

Only comment the why, never the what. No docstrings for obvious functions. A one-line comment is the maximum.

## Testing

Use `github.com/stretchr/testify/require` for assertions. Prefer `require` over `assert` so test functions fail fast on the first unmet condition.

```go
require.NoError(t, err)
require.Equal(t, http.StatusCreated, resp.StatusCode)
```

- Every new CLI flag or command wiring needs a real-binary test in `cmd/...`, because unit tests do not protect it.
- Tests that run the binary must use a temporary `HOME`.
- When a test fails, fix the code. Change the test's data only if the test is wrong.
- Real-binary tests share one binary, built by `TestMain`, so use `binPath` rather than building per test. CI builds it with `-race` through `GOFLAGS`.
- Generate ephemeral RSA keys in memory during tests and stub GitHub API calls with `httptest.Server`; never commit test keys or depend on live network calls.
