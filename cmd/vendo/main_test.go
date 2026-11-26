package main

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/wolfeidau/vendo/internal/config"
	"github.com/wolfeidau/vendo/internal/github"
)

var binPath string

func TestMain(m *testing.M) {
	tmpDir, err := os.MkdirTemp("", "vendo-bin-test-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create temp dir: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	binPath = filepath.Join(tmpDir, "vendo")
	build := exec.Command("go", "build", "-o", binPath, ".")
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "build vendo binary: %v\n", err)
		os.Exit(1)
	}

	os.Exit(m.Run())
}

type testFixture struct {
	t           *testing.T
	homeDir     string
	xdgDir      string
	binDir      string
	keyPath     string
	opCallsFile string
	pubKey      *rsa.PublicKey
	server      *httptest.Server

	mu             sync.Mutex
	requests       []recordedRequest
	app            github.App
	installation   github.Installation
	mintedToken    string
	revokedTokens  []string
	lastTokenReq   github.AccessTokenRequest
	revokeNotified chan string
}

type recordedRequest struct {
	Method string
	Path   string
	Auth   string
}

func newTestFixture(t *testing.T) *testFixture {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})

	base := t.TempDir()
	homeDir := filepath.Join(base, "home")
	xdgDir := filepath.Join(base, "xdg")
	binDir := filepath.Join(base, "bin")
	require.NoError(t, os.MkdirAll(homeDir, 0o700))
	require.NoError(t, os.MkdirAll(xdgDir, 0o700))
	require.NoError(t, os.MkdirAll(binDir, 0o700))

	keyPath := filepath.Join(base, "app.pem")
	require.NoError(t, os.WriteFile(keyPath, pemBytes, 0o600))

	opCallsFile := filepath.Join(base, "op-calls.log")
	opScript := fmt.Sprintf("#!/bin/sh\necho \"$@\" >> %q\ncat %q\n", opCallsFile, keyPath)
	opPath := filepath.Join(binDir, "op")
	require.NoError(t, os.WriteFile(opPath, []byte(opScript), 0o700))

	f := &testFixture{
		t:           t,
		homeDir:     homeDir,
		xdgDir:      xdgDir,
		binDir:      binDir,
		keyPath:     keyPath,
		opCallsFile: opCallsFile,
		pubKey:      &key.PublicKey,
		app: github.App{
			ID:   12345,
			Slug: "vendo-bot",
			Name: "Vendo Bot",
		},
		installation: github.Installation{
			ID: 98765,
			Account: github.Account{
				Login: "acme",
				ID:    1,
				Type:  "Organization",
			},
			Permissions: map[string]string{
				"contents":      "read",
				"metadata":      "read",
				"pull_requests": "write",
				"issues":        "write",
			},
			RepositorySelection: "selected",
		},
		mintedToken:    "ghs_ephemeral_test_token_12345",
		revokeNotified: make(chan string, 4),
	}

	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.server.Close)

	return f
}

func (f *testFixture) handle(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	f.mu.Lock()
	f.requests = append(f.requests, recordedRequest{
		Method: r.Method,
		Path:   r.URL.Path,
		Auth:   auth,
	})
	app := f.app
	inst := f.installation
	minted := f.mintedToken
	f.mu.Unlock()

	if r.Header.Get("Accept") != "application/vnd.github+json" ||
		r.Header.Get("X-GitHub-Api-Version") != "2022-11-28" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	switch {
	case r.Method == http.MethodDelete && r.URL.Path == "/installation/token":
		tok := strings.TrimPrefix(auth, "Bearer ")
		f.mu.Lock()
		f.revokedTokens = append(f.revokedTokens, tok)
		f.mu.Unlock()
		select {
		case f.revokeNotified <- tok:
		default:
		}
		w.WriteHeader(http.StatusNoContent)
		return

	case r.Method == http.MethodGet && r.URL.Path == "/app":
		if !f.verifyJWT(strings.TrimPrefix(auth, "Bearer ")) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(app)
		return

	case r.Method == http.MethodGet && r.URL.Path == fmt.Sprintf("/app/installations/%d", inst.ID):
		if !f.verifyJWT(strings.TrimPrefix(auth, "Bearer ")) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(inst)
		return

	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/repos/") && strings.HasSuffix(r.URL.Path, "/installation"):
		if !f.verifyJWT(strings.TrimPrefix(auth, "Bearer ")) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(inst)
		return

	case r.Method == http.MethodPost && r.URL.Path == fmt.Sprintf("/app/installations/%d/access_tokens", inst.ID):
		if !f.verifyJWT(strings.TrimPrefix(auth, "Bearer ")) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var req github.AccessTokenRequest
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.lastTokenReq = req
		f.mu.Unlock()

		repos := make([]github.Repository, 0, len(req.Repositories))
		for i, name := range req.Repositories {
			repos = append(repos, github.Repository{
				ID:       int64(i + 1),
				Name:     name,
				FullName: "acme/" + name,
			})
		}

		resp := github.InstallationToken{
			Token:        minted,
			ExpiresAt:    time.Now().UTC().Add(time.Hour).Truncate(time.Second),
			Permissions:  req.Permissions,
			Repositories: repos,
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(resp)
		return
	}

	w.WriteHeader(http.StatusNotFound)
}

func (f *testFixture) verifyJWT(token string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	return rsa.VerifyPKCS1v15(f.pubKey, crypto.SHA256, digest[:], sig) == nil
}

func (f *testFixture) env(extra ...string) []string {
	base := []string{
		"HOME=" + f.homeDir,
		"XDG_CONFIG_HOME=" + f.xdgDir,
		"PATH=" + f.binDir + string(os.PathListSeparator) + os.Getenv("PATH"),
	}
	return append(base, extra...)
}

func (f *testFixture) run(stdin string, extraEnv []string, args ...string) (stdout, stderr string, exitCode int) {
	f.t.Helper()
	cmd := exec.Command(binPath, args...)
	cmd.Env = f.env(extraEnv...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	err := cmd.Run()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return outBuf.String(), errBuf.String(), exitErr.ExitCode()
		}
		f.t.Fatalf("failed to run vendo: %v", err)
	}
	return outBuf.String(), errBuf.String(), 0
}

func (f *testFixture) opCallCount() int {
	data, err := os.ReadFile(f.opCallsFile)
	if err != nil {
		return 0
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return 0
	}
	return len(strings.Split(trimmed, "\n"))
}

func (f *testFixture) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func (f *testFixture) defaultConfigPath() string {
	return filepath.Join(f.xdgDir, "vendo", "config.yaml")
}

func TestVersionFlag(t *testing.T) {
	f := newTestFixture(t)
	stdout, stderr, code := f.run("", nil, "--version")
	require.Equal(t, 0, code)
	require.Equal(t, "dev (none, unknown)\n", stdout)
	require.Empty(t, stderr)
}

func TestInitCommand(t *testing.T) {
	f := newTestFixture(t)

	stdout, stderr, code := f.run("", nil,
		"--api-url", f.server.URL,
		"--profile", "reviewer",
		"--app-id", "12345",
		"--key-ref", "op://vault/item/field",
		"init", "-r", "acme/widgets",
	)
	require.Equal(t, 0, code, "stderr: %s", stderr)
	require.Contains(t, stdout, "vendo-bot")
	require.Contains(t, stdout, "98765")
	require.NotContains(t, stdout, "op://vault/item/field")
	require.Equal(t, 1, f.opCallCount())

	cfgPath := f.defaultConfigPath()
	info, err := os.Stat(cfgPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	raw, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	var cfg config.Config
	require.NoError(t, yaml.Unmarshal(raw, &cfg))
	require.Equal(t, "reviewer", cfg.DefaultProfile)
	require.Equal(t, int64(98765), cfg.Profiles["reviewer"].InstallationID)
	require.Equal(t, "write", cfg.Profiles["reviewer"].Permissions["pull_requests"])

	// Duplicate profile without --force fails upfront before calling op.
	prevOpCalls := f.opCallCount()
	prevReqs := f.requestCount()
	_, stderr, code = f.run("", nil,
		"--api-url", f.server.URL,
		"--profile", "reviewer",
		"--app-id", "12345",
		"--key-ref", "op://vault/item/field",
		"init", "-r", "acme/widgets",
	)
	require.NotEqual(t, 0, code)
	require.Contains(t, stderr, "already exists")
	require.Equal(t, prevOpCalls, f.opCallCount())
	require.Equal(t, prevReqs, f.requestCount())

	// Add a custom preset to verify --force preserves presets while refreshing permissions.
	prof := cfg.Profiles["reviewer"]
	prof.Presets = map[string]map[string]string{"triage": {"issues": "write"}}
	cfg.Profiles["reviewer"] = prof
	require.NoError(t, config.Save(cfgPath, &cfg))

	f.mu.Lock()
	f.installation.Permissions = map[string]string{
		"contents":      "read",
		"metadata":      "read",
		"pull_requests": "read",
	}
	f.mu.Unlock()

	_, stderr, code = f.run("", nil, "init", "--profile", "reviewer", "--force")
	require.Equal(t, 0, code, "stderr: %s", stderr)

	updated, err := config.Load(cfgPath, true)
	require.NoError(t, err)
	require.Equal(t, "read", updated.Profiles["reviewer"].Permissions["pull_requests"])
	require.Equal(t, map[string]string{"issues": "write"}, updated.Profiles["reviewer"].Presets["triage"])
}

func TestPermsCommand(t *testing.T) {
	f := newTestFixture(t)
	cfgPath := f.defaultConfigPath()
	require.NoError(t, config.Save(cfgPath, &config.Config{
		DefaultProfile: "reviewer",
		Profiles: map[string]config.Profile{
			"reviewer": {
				AppID:          "12345",
				KeyRef:         "op://vault/item/field",
				InstallationID: 98765,
				APIURL:         f.server.URL,
				Permissions: map[string]string{
					"contents":      "read",
					"metadata":      "read",
					"pull_requests": "write",
				},
			},
			"no-perms": {
				AppID:  "12345",
				KeyRef: "op://vault/item/field",
				APIURL: f.server.URL,
			},
		},
	}))

	// Offline text makes zero op or API calls.
	stdout, stderr, code := f.run("", nil, "perms")
	require.Equal(t, 0, code, "stderr: %s", stderr)
	require.Contains(t, stdout, "Permissions (config, profile: reviewer, installation: 98765):")
	require.Contains(t, stdout, "pull_requests: write")
	require.Contains(t, stdout, "pr-review:")
	require.Equal(t, 0, f.opCallCount())
	require.Equal(t, 0, f.requestCount())

	// Offline JSON.
	stdout, stderr, code = f.run("", nil, "perms", "--format", "json")
	require.Equal(t, 0, code, "stderr: %s", stderr)
	var out struct {
		Source      string                       `json:"source"`
		Profile     string                       `json:"profile"`
		Permissions map[string]string            `json:"permissions"`
		Presets     map[string]map[string]string `json:"presets"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &out))
	require.Equal(t, "config", out.Source)
	require.Equal(t, "write", out.Permissions["pull_requests"])
	require.Equal(t, 0, f.opCallCount())

	// Missing recorded permissions returns actionable error.
	_, stderr, code = f.run("", nil, "--profile", "no-perms", "perms")
	require.NotEqual(t, 0, code)
	require.Contains(t, stderr, "vendo init")
	require.Contains(t, stderr, "vendo perms --live")
	require.Equal(t, 0, f.opCallCount())

	// Live query invokes op and GitHub API.
	stdout, stderr, code = f.run("", nil, "perms", "--live", "--format", "json")
	require.Equal(t, 0, code, "stderr: %s", stderr)
	require.NoError(t, json.Unmarshal([]byte(stdout), &out))
	require.Equal(t, "live", out.Source)
	require.Equal(t, "write", out.Permissions["issues"])
	require.Equal(t, 1, f.opCallCount())
}

func TestCheckCommandAndDriftWarning(t *testing.T) {
	f := newTestFixture(t)
	cfgPath := f.defaultConfigPath()
	require.NoError(t, config.Save(cfgPath, &config.Config{
		DefaultProfile: "reviewer",
		Profiles: map[string]config.Profile{
			"reviewer": {
				AppID:          "12345",
				KeyRef:         "op://vault/item/field",
				InstallationID: 98765,
				APIURL:         f.server.URL,
				Permissions: map[string]string{
					"contents":      "write", // differs from live "read"
					"metadata":      "read",
					"pull_requests": "write",
					"deployments":   "read", // removed in live
				},
			},
		},
	}))

	// Check by installation-id (from profile) detects drift.
	stdout, stderr, code := f.run("", nil, "check")
	require.Equal(t, 0, code, "stderr: %s", stderr)
	require.Contains(t, stdout, "App: vendo-bot (ID: 12345)")
	require.Contains(t, stdout, "Installation ID: 98765")
	require.Contains(t, stdout, "Warning: permission drift")
	require.Contains(t, stdout, "contents: config=write, live=read")
	require.Contains(t, stdout, "deployments: config=read, live=(removed)")
	require.Contains(t, stdout, "issues: config=(missing), live=write")

	// Check by -r flag uses /repos/{owner}/{repo}/installation.
	stdout, stderr, code = f.run("", nil, "check", "-r", "acme/widgets")
	require.Equal(t, 0, code, "stderr: %s", stderr)
	require.Contains(t, stdout, "Installation ID: 98765")

	f.mu.Lock()
	last := f.requests[len(f.requests)-1]
	f.mu.Unlock()
	require.Equal(t, "/repos/acme/widgets/installation", last.Path)
}

func TestTokenCommand(t *testing.T) {
	f := newTestFixture(t)
	cfgPath := f.defaultConfigPath()
	require.NoError(t, config.Save(cfgPath, &config.Config{
		DefaultProfile: "reviewer",
		Profiles: map[string]config.Profile{
			"reviewer": {
				AppID:          "12345",
				KeyRef:         "op://vault/item/field",
				InstallationID: 98765,
				APIURL:         f.server.URL,
				Permissions: map[string]string{
					"contents":      "read",
					"metadata":      "read",
					"pull_requests": "write",
					"issues":        "write",
				},
			},
		},
	}))

	// Raw format with preset + --perm override.
	stdout, stderr, code := f.run("", nil,
		"token", "-r", "acme/widgets", "--preset", "pr-review", "-p", "pull_requests=read",
	)
	require.Equal(t, 0, code, "stderr: %s", stderr)
	require.Equal(t, f.mintedToken+"\n", stdout)

	f.mu.Lock()
	tokenReq := f.lastTokenReq
	f.mu.Unlock()
	require.Equal(t, []string{"widgets"}, tokenReq.Repositories)
	require.Equal(t, "read", tokenReq.Permissions["pull_requests"])
	require.Equal(t, "read", tokenReq.Permissions["contents"])

	// JSON format.
	stdout, stderr, code = f.run("", nil,
		"token", "--all-repos", "--preset", "pr-comment", "--format", "json",
	)
	require.Equal(t, 0, code, "stderr: %s", stderr)
	var tok github.InstallationToken
	require.NoError(t, json.Unmarshal([]byte(stdout), &tok))
	require.Equal(t, f.mintedToken, tok.Token)
	require.Equal(t, "write", tok.Permissions["issues"])

	f.mu.Lock()
	allReposReq := f.lastTokenReq
	f.mu.Unlock()
	require.Empty(t, allReposReq.Repositories)

	// Recorded permission cap rejection upfront (before op or API).
	prevOpCalls := f.opCallCount()
	prevReqs := f.requestCount()
	_, stderr, code = f.run("", nil,
		"token", "-r", "acme/widgets", "-p", "contents=write",
	)
	require.NotEqual(t, 0, code)
	require.Contains(t, stderr, "exceeds profile grant")
	require.Equal(t, prevOpCalls, f.opCallCount())
	require.Equal(t, prevReqs, f.requestCount())
}

func TestExecCommand(t *testing.T) {
	f := newTestFixture(t)
	cfgPath := f.defaultConfigPath()
	require.NoError(t, config.Save(cfgPath, &config.Config{
		DefaultProfile: "reviewer",
		Profiles: map[string]config.Profile{
			"reviewer": {
				AppID:          "12345",
				KeyRef:         "op://vault/item/field",
				InstallationID: 98765,
				APIURL:         f.server.URL,
				Permissions: map[string]string{
					"contents":      "read",
					"metadata":      "read",
					"pull_requests": "write",
				},
			},
		},
	}))

	t.Run("injects GH_TOKEN, unsets GITHUB_TOKEN, passes flags after --, revokes on exit", func(t *testing.T) {
		stdout, stderr, code := f.run("",
			[]string{"GITHUB_TOKEN=ghp_personal_should_be_removed", "GH_TOKEN=ghp_old"},
			"exec", "-r", "acme/widgets", "--preset", "pr-review", "--",
			"sh", "-c", `printf "GH=%s|GITHUB=%s|ARGS=%s\n" "$GH_TOKEN" "${GITHUB_TOKEN:-unset}" "$*"`,
			"--", "--approve", "-b", "LGTM",
		)
		require.Equal(t, 0, code, "stderr: %s", stderr)
		require.Equal(t, fmt.Sprintf("GH=%s|GITHUB=unset|ARGS=--approve -b LGTM\n", f.mintedToken), stdout)

		f.mu.Lock()
		revoked := append([]string(nil), f.revokedTokens...)
		f.mu.Unlock()
		require.Contains(t, revoked, f.mintedToken)
	})

	t.Run("propagates child exit code without duplicate error log", func(t *testing.T) {
		_, stderr, code := f.run("", nil,
			"exec", "-r", "acme/widgets", "--preset", "pr-review", "--",
			"sh", "-c", "exit 42",
		)
		require.Equal(t, 42, code)
		require.Empty(t, stderr)
	})

	t.Run("no-revoke skips DELETE /installation/token", func(t *testing.T) {
		f.mu.Lock()
		before := len(f.revokedTokens)
		f.mu.Unlock()

		_, stderr, code := f.run("", nil,
			"exec", "--no-revoke", "-r", "acme/widgets", "--preset", "pr-review", "--",
			"sh", "-c", "true",
		)
		require.Equal(t, 0, code, "stderr: %s", stderr)

		f.mu.Lock()
		after := len(f.revokedTokens)
		f.mu.Unlock()
		require.Equal(t, before, after)
	})

	t.Run("forwards SIGTERM and revokes token with detached context", func(t *testing.T) {
		readyFile := filepath.Join(t.TempDir(), "ready")
		childScript := fmt.Sprintf("touch %q; exec sleep 30", readyFile)

		// Drain any prior notifications.
		for len(f.revokeNotified) > 0 {
			<-f.revokeNotified
		}

		cmd := exec.Command(binPath,
			"exec", "-r", "acme/widgets", "--preset", "pr-review", "--",
			"sh", "-c", childScript,
		)
		cmd.Env = f.env()
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		require.NoError(t, cmd.Start())

		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(readyFile); err == nil {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		_, statErr := os.Stat(readyFile)
		require.NoError(t, statErr, "child process did not become ready")

		require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))

		waitErr := cmd.Wait()
		var exitErr *exec.ExitError
		require.ErrorAs(t, waitErr, &exitErr)
		require.Equal(t, 128+int(syscall.SIGTERM), exitErr.ExitCode())
		require.Empty(t, stderr.String())

		select {
		case revokedTok := <-f.revokeNotified:
			require.Equal(t, f.mintedToken, revokedTok)
		case <-time.After(3 * time.Second):
			t.Fatal("expected DELETE /installation/token after SIGTERM")
		}
	})

	t.Run("rejects --key-file - upfront", func(t *testing.T) {
		prevOp := f.opCallCount()
		prevReq := f.requestCount()
		_, stderr, code := f.run("ignored", nil,
			"--key-file", "-", "exec", "-r", "acme/widgets", "--preset", "pr-review", "--", "true",
		)
		require.NotEqual(t, 0, code)
		require.Contains(t, stderr, "--key-file -")
		require.Equal(t, prevOp, f.opCallCount())
		require.Equal(t, prevReq, f.requestCount())
	})
}

func TestRevokeCommand(t *testing.T) {
	f := newTestFixture(t)

	// Works without app-id or key source using GH_TOKEN.
	_, stderr, code := f.run("",
		[]string{"GH_TOKEN=ghs_env_revoke_token"},
		"--api-url", f.server.URL, "revoke",
	)
	require.Equal(t, 0, code, "stderr: %s", stderr)

	f.mu.Lock()
	require.Equal(t, "ghs_env_revoke_token", f.revokedTokens[len(f.revokedTokens)-1])
	f.mu.Unlock()

	// --token-stdin takes precedence over GH_TOKEN.
	_, stderr, code = f.run("ghs_stdin_revoke_token\n",
		[]string{"GH_TOKEN=ghs_env_should_lose"},
		"--api-url", f.server.URL, "revoke", "--token-stdin",
	)
	require.Equal(t, 0, code, "stderr: %s", stderr)

	f.mu.Lock()
	require.Equal(t, "ghs_stdin_revoke_token", f.revokedTokens[len(f.revokedTokens)-1])
	f.mu.Unlock()

	// Empty input fails immediately without blocking on stdin.
	_, stderr, code = f.run("", nil, "--api-url", f.server.URL, "revoke")
	require.NotEqual(t, 0, code)
	require.Contains(t, stderr, "GH_TOKEN")

	_, stderr, code = f.run("   \n", []string{"GH_TOKEN=ghs_ignored"}, "--api-url", f.server.URL, "revoke", "--token-stdin")
	require.NotEqual(t, 0, code)
	require.Contains(t, stderr, "stdin")
}

func TestKeySourceLayerOverride(t *testing.T) {
	f := newTestFixture(t)
	cfgPath := f.defaultConfigPath()
	require.NoError(t, config.Save(cfgPath, &config.Config{
		DefaultProfile: "reviewer",
		Profiles: map[string]config.Profile{
			"reviewer": {
				AppID:          "12345",
				KeyRef:         "op://vault/item/field",
				InstallationID: 98765,
				APIURL:         f.server.URL,
				Permissions: map[string]string{
					"contents":      "read",
					"metadata":      "read",
					"pull_requests": "write",
				},
			},
		},
	}))

	// CLI --key-file overrides config key_ref and VENDO_KEY_REF without invoking op.
	stdout, stderr, code := f.run("",
		[]string{"VENDO_KEY_REF=op://other/item/field"},
		"--key-file", f.keyPath,
		"token", "-r", "acme/widgets", "--preset", "pr-review",
	)
	require.Equal(t, 0, code, "stderr: %s", stderr)
	require.Equal(t, f.mintedToken+"\n", stdout)
	require.Equal(t, 0, f.opCallCount())
}

func TestUpfrontValidationFailuresNoExternalIO(t *testing.T) {
	f := newTestFixture(t)

	cases := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "missing permissions on token",
			args:    []string{"--api-url", f.server.URL, "--app-id", "12345", "--key-ref", "op://v/i/f", "token", "-r", "acme/widgets"},
			wantErr: "at least one permission",
		},
		{
			name:    "invalid op ref",
			args:    []string{"--api-url", f.server.URL, "--app-id", "12345", "--key-ref", "bad://ref", "token", "-r", "acme/widgets", "--preset", "pr-review"},
			wantErr: "op://",
		},
		{
			name:    "mixing repo and all-repos",
			args:    []string{"--api-url", f.server.URL, "--app-id", "12345", "--key-ref", "op://v/i/f", "--installation-id", "98765", "token", "-r", "acme/widgets", "--all-repos", "--preset", "pr-review"},
			wantErr: "cannot combine --repo and --all-repos",
		},
		{
			name:    "all-repos without installation-id",
			args:    []string{"--api-url", f.server.URL, "--app-id", "12345", "--key-ref", "op://v/i/f", "token", "--all-repos", "--preset", "pr-review"},
			wantErr: "--all-repos requires --installation-id",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, stderr, code := f.run("", nil, tc.args...)
			require.NotEqual(t, 0, code)
			require.Contains(t, stderr, tc.wantErr)
			require.Equal(t, 0, f.opCallCount())
			require.Equal(t, 0, f.requestCount())
		})
	}
}
