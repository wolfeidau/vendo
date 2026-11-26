package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alecthomas/kong"
	"github.com/stretchr/testify/require"

	"github.com/wolfeidau/vendo/internal/config"
	"github.com/wolfeidau/vendo/internal/github"
)

func mapEnv(m map[string]string) func(string) string {
	return func(k string) string {
		return m[k]
	}
}

func newTestGlobals(env map[string]string, stdin string) (*Globals, *bytes.Buffer, *bytes.Buffer) {
	var stdout, stderr bytes.Buffer
	g := &Globals{
		Logger:  slog.New(slog.NewTextHandler(&stderr, nil)),
		Version: "1.2.3",
		Commit:  "abc1234",
		Date:    "2026-01-02",
		Stdin:   strings.NewReader(stdin),
		Stdout:  &stdout,
		Stderr:  &stderr,
		Getenv:  mapEnv(env),
	}
	return g, &stdout, &stderr
}

func ptrInt64(v int64) *int64 {
	return &v
}

func TestResolvePrecedence(t *testing.T) {
	profile := &config.Profile{
		AppID:          "profile-app",
		KeyRef:         "op://vault/item/field",
		InstallationID: 100,
		APIURL:         "https://ghe.example.com/api/v3",
	}

	t.Run("CLI overrides env and profile", func(t *testing.T) {
		g, _, _ := newTestGlobals(map[string]string{
			"VENDO_APP_ID":          "env-app",
			"VENDO_KEY_REF":         "op://env-vault/env-item/env-field",
			"VENDO_INSTALLATION_ID": "200",
			"VENDO_API_URL":         "https://env.example.com",
		}, "")

		cli := &CLI{
			AppID:          "cli-app",
			KeyFile:        "/tmp/key.pem",
			InstallationID: ptrInt64(300),
			APIURL:         "https://cli.example.com",
			Timeout:        15 * time.Second,
		}

		auth, err := resolveAuth(cli, g, profile, false)
		require.NoError(t, err)
		require.Equal(t, "cli-app", auth.appID)
		require.Empty(t, auth.keyRef)
		require.Equal(t, "/tmp/key.pem", auth.keyFile)

		instID, explicit, err := resolveInstallationID(cli, g, profile)
		require.NoError(t, err)
		require.True(t, explicit)
		require.Equal(t, int64(300), instID)

		require.Equal(t, "https://cli.example.com", resolveAPIURL(cli, g, profile))
	})

	t.Run("env overrides profile", func(t *testing.T) {
		g, _, _ := newTestGlobals(map[string]string{
			"VENDO_APP_ID":          "env-app",
			"VENDO_KEY_FILE":        "/tmp/env-key.pem",
			"VENDO_INSTALLATION_ID": "200",
			"VENDO_API_URL":         "https://env.example.com",
		}, "")

		cli := &CLI{Timeout: 15 * time.Second}

		auth, err := resolveAuth(cli, g, profile, false)
		require.NoError(t, err)
		require.Equal(t, "env-app", auth.appID)
		require.Empty(t, auth.keyRef)
		require.Equal(t, "/tmp/env-key.pem", auth.keyFile)

		instID, explicit, err := resolveInstallationID(cli, g, profile)
		require.NoError(t, err)
		require.True(t, explicit)
		require.Equal(t, int64(200), instID)

		require.Equal(t, "https://env.example.com", resolveAPIURL(cli, g, profile))
	})

	t.Run("profile overrides defaults", func(t *testing.T) {
		g, _, _ := newTestGlobals(nil, "")
		cli := &CLI{Timeout: 15 * time.Second}

		auth, err := resolveAuth(cli, g, profile, false)
		require.NoError(t, err)
		require.Equal(t, "profile-app", auth.appID)
		require.Equal(t, "op://vault/item/field", auth.keyRef)
		require.Empty(t, auth.keyFile)

		instID, explicit, err := resolveInstallationID(cli, g, profile)
		require.NoError(t, err)
		require.False(t, explicit)
		require.Equal(t, int64(100), instID)

		require.Equal(t, "https://ghe.example.com/api/v3", resolveAPIURL(cli, g, profile))
	})

	t.Run("default API URL when unset in all layers", func(t *testing.T) {
		g, _, _ := newTestGlobals(nil, "")
		cli := &CLI{Timeout: 15 * time.Second}
		require.Equal(t, github.DefaultBaseURL, resolveAPIURL(cli, g, nil))
	})

	t.Run("same-layer key source conflict rejected", func(t *testing.T) {
		g, _, _ := newTestGlobals(map[string]string{
			"VENDO_KEY_REF":  "op://v/i/f",
			"VENDO_KEY_FILE": "/tmp/k.pem",
		}, "")
		cli := &CLI{AppID: "123", Timeout: 15 * time.Second}
		_, err := resolveAuth(cli, g, nil, false)
		require.ErrorContains(t, err, "VENDO_KEY_REF")

		g2, _, _ := newTestGlobals(nil, "")
		cli2 := &CLI{AppID: "123", KeyRef: "op://v/i/f", KeyFile: "/tmp/k.pem", Timeout: 15 * time.Second}
		_, err = resolveAuth(cli2, g2, nil, false)
		require.ErrorContains(t, err, "--key-ref")
	})
}

func TestUpfrontValidation(t *testing.T) {
	t.Run("invalid op ref rejected", func(t *testing.T) {
		g, _, _ := newTestGlobals(nil, "")
		cli := &CLI{AppID: "123", KeyRef: "not-op-ref", Timeout: 15 * time.Second}
		_, err := resolveAuth(cli, g, nil, false)
		require.ErrorContains(t, err, "op://")
	})

	t.Run("stdin key rejected for exec", func(t *testing.T) {
		g, _, _ := newTestGlobals(nil, "")
		cli := &CLI{AppID: "123", KeyFile: "-", Timeout: 15 * time.Second}
		_, err := resolveAuth(cli, g, nil, true)
		require.ErrorContains(t, err, "--key-file -")
	})

	t.Run("non-positive timeout rejected", func(t *testing.T) {
		g, _, _ := newTestGlobals(nil, "")
		cli := &CLI{Timeout: 0}
		_, _, err := newGitHubClient(cli, g, nil)
		require.ErrorContains(t, err, "--timeout")
	})

	t.Run("non-positive installation ID rejected", func(t *testing.T) {
		g, _, _ := newTestGlobals(nil, "")
		cli := &CLI{InstallationID: ptrInt64(0)}
		_, _, err := resolveInstallationID(cli, g, nil)
		require.ErrorContains(t, err, "positive")

		gEnv, _, _ := newTestGlobals(map[string]string{"VENDO_INSTALLATION_ID": "-5"}, "")
		_, _, err = resolveInstallationID(&CLI{}, gEnv, nil)
		require.ErrorContains(t, err, "VENDO_INSTALLATION_ID")
	})

	t.Run("prepareScopedTokenRequest rejects mixing repo and all-repos", func(t *testing.T) {
		g, _, _ := newTestGlobals(nil, "")
		cli := &CLI{AppID: "123", KeyFile: "/tmp/k.pem", InstallationID: ptrInt64(10), Timeout: 15 * time.Second}
		_, err := prepareScopedTokenRequest(context.Background(), cli, g, nil, []string{"org/repo"}, true, []string{"contents=read"}, "", false)
		require.ErrorContains(t, err, "cannot combine --repo and --all-repos")
	})

	t.Run("prepareScopedTokenRequest rejects all-repos without installation-id", func(t *testing.T) {
		g, _, _ := newTestGlobals(nil, "")
		cli := &CLI{AppID: "123", KeyFile: "/tmp/k.pem", Timeout: 15 * time.Second}
		_, err := prepareScopedTokenRequest(context.Background(), cli, g, nil, nil, true, []string{"contents=read"}, "", false)
		require.ErrorContains(t, err, "--all-repos requires --installation-id")
	})

	t.Run("prepareScopedTokenRequest rejects missing permissions", func(t *testing.T) {
		g, _, _ := newTestGlobals(nil, "")
		cli := &CLI{AppID: "123", KeyFile: "/tmp/k.pem", Timeout: 15 * time.Second}
		_, err := prepareScopedTokenRequest(context.Background(), cli, g, nil, []string{"org/repo"}, false, nil, "", false)
		require.ErrorContains(t, err, "at least one permission")
	})
}

func TestKongPassthroughParsing(t *testing.T) {
	t.Run("with double dash preserves all child flags", func(t *testing.T) {
		var cli CLI
		g, _, _ := newTestGlobals(nil, "")
		parser, err := NewParser(&cli, g, kong.Exit(func(int) {}))
		require.NoError(t, err)

		_, err = parser.Parse([]string{
			"exec", "--preset", "pr-review", "-r", "org/repo", "--",
			"gh", "pr", "review", "42", "--approve", "-b", "LGTM", "-r", "ignored",
		})
		require.NoError(t, err)
		require.Equal(t, "pr-review", cli.Exec.Preset)
		require.Equal(t, []string{"org/repo"}, cli.Exec.Repos)
		require.Equal(t, []string{"gh", "pr", "review", "42", "--approve", "-b", "LGTM", "-r", "ignored"}, stripLeadingDashDash(cli.Exec.Args))
	})

	t.Run("without double dash passes trailing flags after command", func(t *testing.T) {
		var cli CLI
		g, _, _ := newTestGlobals(nil, "")
		parser, err := NewParser(&cli, g, kong.Exit(func(int) {}))
		require.NoError(t, err)

		_, err = parser.Parse([]string{
			"exec", "--preset", "pr-review", "gh", "pr", "review", "--approve",
		})
		require.NoError(t, err)
		require.Equal(t, []string{"gh", "pr", "review", "--approve"}, stripLeadingDashDash(cli.Exec.Args))
	})

	t.Run("unknown flag before command is rejected", func(t *testing.T) {
		var cli CLI
		g, _, _ := newTestGlobals(nil, "")
		parser, err := NewParser(&cli, g, kong.Exit(func(int) {}))
		require.NoError(t, err)

		_, err = parser.Parse([]string{"exec", "--unknown-flag", "--", "gh", "pr", "list"})
		require.Error(t, err)
	})
}

func TestBuildChildEnv(t *testing.T) {
	in := []string{
		"PATH=/usr/bin",
		"GH_TOKEN=old-gh",
		"GITHUB_TOKEN=personal-token",
		"HOME=/tmp/home",
	}
	out := buildChildEnv(in, "v1.newtoken")
	require.Contains(t, out, "PATH=/usr/bin")
	require.Contains(t, out, "HOME=/tmp/home")
	require.Contains(t, out, "GH_TOKEN=v1.newtoken")
	require.NotContains(t, out, "GH_TOKEN=old-gh")
	require.NotContains(t, out, "GITHUB_TOKEN=personal-token")
}

func TestPermsFormattingAndOffline(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	err := config.Save(cfgPath, &config.Config{
		DefaultProfile: "reviewer",
		Profiles: map[string]config.Profile{
			"reviewer": {
				AppID:          "123",
				KeyRef:         "op://v/i/f",
				InstallationID: 456,
				Permissions: map[string]string{
					"pull_requests": "write",
					"contents":      "read",
					"metadata":      "read",
				},
				Presets: map[string]map[string]string{
					"custom": {"contents": "read"},
				},
			},
			"empty": {
				AppID:  "123",
				KeyRef: "op://v/i/f",
			},
		},
	})
	require.NoError(t, err)

	t.Run("offline text", func(t *testing.T) {
		g, stdout, _ := newTestGlobals(nil, "")
		cli := &CLI{Config: cfgPath, Timeout: 15 * time.Second}
		cmd := &PermsCmd{Format: "text"}
		require.NoError(t, cmd.Run(context.Background(), cli, g))

		text := stdout.String()
		require.Contains(t, text, "Permissions (config, profile: reviewer, installation: 456):")
		require.Contains(t, text, "  contents: read\n  metadata: read\n  pull_requests: write\n")
		require.Contains(t, text, "  custom: contents=read\n")
		require.Contains(t, text, "  pr-review: contents=read, metadata=read, pull_requests=write\n")
	})

	t.Run("offline json", func(t *testing.T) {
		g, stdout, _ := newTestGlobals(nil, "")
		cli := &CLI{Config: cfgPath, Timeout: 15 * time.Second}
		cmd := &PermsCmd{Format: "json"}
		require.NoError(t, cmd.Run(context.Background(), cli, g))

		var out permsOutput
		require.NoError(t, json.Unmarshal(stdout.Bytes(), &out))
		require.Equal(t, "config", out.Source)
		require.Equal(t, "reviewer", out.Profile)
		require.Equal(t, int64(456), out.InstallationID)
		require.Equal(t, "write", out.Permissions["pull_requests"])
		require.Equal(t, map[string]string{"contents": "read"}, out.Presets["custom"])
	})

	t.Run("missing recorded perms returns actionable error", func(t *testing.T) {
		g, _, _ := newTestGlobals(nil, "")
		cli := &CLI{Config: cfgPath, Profile: "empty", Timeout: 15 * time.Second}
		cmd := &PermsCmd{Format: "text"}
		err := cmd.Run(context.Background(), cli, g)
		require.ErrorContains(t, err, "vendo init")
		require.ErrorContains(t, err, "vendo perms --live")
	})
}

func TestComputePermissionDrift(t *testing.T) {
	recorded := map[string]string{
		"contents":      "read",
		"metadata":      "read",
		"pull_requests": "write",
		"issues":        "read",
	}
	live := map[string]string{
		"contents":      "write",
		"metadata":      "read",
		"pull_requests": "write",
		"checks":        "read",
	}

	drifts := computePermissionDrift(recorded, live)
	require.Equal(t, []string{
		"checks: config=(missing), live=read",
		"contents: config=read, live=write",
		"issues: config=read, live=(removed)",
	}, drifts)

	require.Empty(t, computePermissionDrift(live, live))
}

func TestReadTokenForRevoke(t *testing.T) {
	t.Run("reads GH_TOKEN when token-stdin is false without touching stdin", func(t *testing.T) {
		g := &Globals{
			Stdin:  errReader{},
			Getenv: mapEnv(map[string]string{"GH_TOKEN": " ghs_from_env \n"}),
		}
		tok, err := readTokenForRevoke(false, g)
		require.NoError(t, err)
		require.Equal(t, "ghs_from_env", tok)
	})

	t.Run("token-stdin takes precedence over GH_TOKEN", func(t *testing.T) {
		g, _, _ := newTestGlobals(map[string]string{"GH_TOKEN": "ghs_from_env"}, "ghs_from_stdin\n")
		tok, err := readTokenForRevoke(true, g)
		require.NoError(t, err)
		require.Equal(t, "ghs_from_stdin", tok)
	})

	t.Run("empty GH_TOKEN fails without blocking on stdin", func(t *testing.T) {
		g := &Globals{
			Stdin:  errReader{},
			Getenv: mapEnv(nil),
		}
		_, err := readTokenForRevoke(false, g)
		require.ErrorContains(t, err, "GH_TOKEN")
	})

	t.Run("empty stdin fails when token-stdin is true", func(t *testing.T) {
		g, _, _ := newTestGlobals(map[string]string{"GH_TOKEN": "ghs_from_env"}, "   \n")
		_, err := readTokenForRevoke(true, g)
		require.ErrorContains(t, err, "stdin")
	})
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}
