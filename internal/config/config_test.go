package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefaultPath(t *testing.T) {
	t.Run("uses XDG_CONFIG_HOME when set", func(t *testing.T) {
		xdgDir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", xdgDir)
		t.Setenv("HOME", t.TempDir())

		got, err := DefaultPath()
		require.NoError(t, err)
		require.Equal(t, filepath.Join(xdgDir, "vendo", "config.yaml"), got)
	})

	t.Run("falls back to HOME/.config", func(t *testing.T) {
		homeDir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("HOME", homeDir)

		got, err := DefaultPath()
		require.NoError(t, err)
		require.Equal(t, filepath.Join(homeDir, ".config", "vendo", "config.yaml"), got)
	})
}

func TestLoad(t *testing.T) {
	t.Run("missing default file returns empty config", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.yaml")

		cfg, err := Load(path, false)
		require.NoError(t, err)
		require.NotNil(t, cfg)
		require.Empty(t, cfg.DefaultProfile)
		require.Empty(t, cfg.Profiles)
	})

	t.Run("missing explicit file fails", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.yaml")

		_, err := Load(path, true)
		require.Error(t, err)
		require.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("empty file returns empty config", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.yaml")
		require.NoError(t, os.WriteFile(path, []byte("# comment only\n"), 0o600))

		cfg, err := Load(path, true)
		require.NoError(t, err)
		require.Empty(t, cfg.DefaultProfile)
		require.Empty(t, cfg.Profiles)
	})

	t.Run("valid config parses all fields", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.yaml")
		raw := `default_profile: reviewer
profiles:
  reviewer:
    app_id: "12345"
    key_ref: "op://<vault>/<item>/<field>"
    installation_id: 987654
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
`
		require.NoError(t, os.WriteFile(path, []byte(raw), 0o600))

		cfg, err := Load(path, true)
		require.NoError(t, err)
		require.Equal(t, "reviewer", cfg.DefaultProfile)
		p, ok := cfg.Profiles["reviewer"]
		require.True(t, ok)
		require.Equal(t, "12345", p.AppID)
		require.Equal(t, "op://<vault>/<item>/<field>", p.KeyRef)
		require.Equal(t, int64(987654), p.InstallationID)
		require.Equal(t, "https://api.github.com", p.APIURL)
		require.Equal(t, map[string]string{
			"contents":      "read",
			"metadata":      "read",
			"pull_requests": "write",
		}, p.Permissions)
		require.Equal(t, map[string]map[string]string{
			"pr-review": {
				"pull_requests": "write",
				"contents":      "read",
				"metadata":      "read",
			},
		}, p.Presets)
	})

	t.Run("rejects unknown YAML field", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.yaml")
		raw := `profiles:
  reviewer:
    app-id: "12345"
`
		require.NoError(t, os.WriteFile(path, []byte(raw), 0o600))

		_, err := Load(path, true)
		require.Error(t, err)
	})

	t.Run("rejects mutually exclusive key_ref and key_file without leaking ref", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.yaml")
		secretRef := "op://secret-vault/secret-item/secret-field"
		raw := "profiles:\n  reviewer:\n    key_ref: \"" + secretRef + "\"\n    key_file: \"/tmp/key.pem\"\n"
		require.NoError(t, os.WriteFile(path, []byte(raw), 0o600))

		_, err := Load(path, true)
		require.Error(t, err)
		require.NotContains(t, err.Error(), secretRef)
	})

	t.Run("rejects invalid permission level in profile", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.yaml")
		raw := `profiles:
  reviewer:
    permissions:
      contents: delete
`
		require.NoError(t, os.WriteFile(path, []byte(raw), 0o600))

		_, err := Load(path, true)
		require.Error(t, err)
	})

	t.Run("rejects invalid permission level in preset", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.yaml")
		raw := `profiles:
  reviewer:
    presets:
      custom:
        contents: owner
`
		require.NoError(t, os.WriteFile(path, []byte(raw), 0o600))

		_, err := Load(path, true)
		require.Error(t, err)
	})

	t.Run("rejects empty preset map", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.yaml")
		raw := `profiles:
  reviewer:
    presets:
      empty: {}
`
		require.NoError(t, os.WriteFile(path, []byte(raw), 0o600))

		_, err := Load(path, true)
		require.Error(t, err)
	})

	t.Run("rejects negative installation_id", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.yaml")
		raw := `profiles:
  reviewer:
    installation_id: -1
`
		require.NoError(t, os.WriteFile(path, []byte(raw), 0o600))

		_, err := Load(path, true)
		require.Error(t, err)
	})
}

func TestSave(t *testing.T) {
	t.Run("creates parent directory with 0700 and file atomically with 0600", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "nested", "vendo")
		path := filepath.Join(dir, "config.yaml")

		cfg := &Config{
			DefaultProfile: "reviewer",
			Profiles: map[string]Profile{
				"reviewer": {
					AppID:          "12345",
					KeyRef:         "op://<vault>/<item>/<field>",
					InstallationID: 67890,
					APIURL:         "https://api.github.com",
					Permissions: map[string]string{
						"contents":      "read",
						"metadata":      "read",
						"pull_requests": "write",
					},
				},
			},
		}

		require.NoError(t, Save(path, cfg))

		dirInfo, err := os.Stat(dir)
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o700), dirInfo.Mode().Perm())

		fileInfo, err := os.Stat(path)
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o600), fileInfo.Mode().Perm())

		entries, err := os.ReadDir(dir)
		require.NoError(t, err)
		require.Len(t, entries, 1)

		loaded, err := Load(path, true)
		require.NoError(t, err)
		require.Equal(t, cfg, loaded)
	})

	t.Run("rejects invalid config without overwriting existing file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.yaml")
		original := &Config{DefaultProfile: "good"}
		require.NoError(t, Save(path, original))

		invalid := &Config{
			Profiles: map[string]Profile{
				"bad": {
					KeyRef:  "op://<vault>/<item>/<field>",
					KeyFile: "/tmp/key.pem",
				},
			},
		}
		require.Error(t, Save(path, invalid))

		loaded, err := Load(path, true)
		require.NoError(t, err)
		require.Equal(t, original, loaded)
	})
}

func TestSelectProfile(t *testing.T) {
	cfg := &Config{
		DefaultProfile: "reviewer",
		Profiles: map[string]Profile{
			"reviewer": {AppID: "111"},
			"triage":   {AppID: "222"},
		},
	}

	t.Run("explicit existing profile", func(t *testing.T) {
		name, p, err := SelectProfile(cfg, "triage", true)
		require.NoError(t, err)
		require.Equal(t, "triage", name)
		require.NotNil(t, p)
		require.Equal(t, "222", p.AppID)
	})

	t.Run("explicit missing profile errors", func(t *testing.T) {
		_, _, err := SelectProfile(cfg, "missing", true)
		require.Error(t, err)
	})

	t.Run("explicit empty profile name errors", func(t *testing.T) {
		_, _, err := SelectProfile(cfg, "", true)
		require.Error(t, err)
	})

	t.Run("falls back to default_profile", func(t *testing.T) {
		name, p, err := SelectProfile(cfg, "", false)
		require.NoError(t, err)
		require.Equal(t, "reviewer", name)
		require.NotNil(t, p)
		require.Equal(t, "111", p.AppID)
	})

	t.Run("empty config returns nil profile", func(t *testing.T) {
		name, p, err := SelectProfile(&Config{}, "", false)
		require.NoError(t, err)
		require.Empty(t, name)
		require.Nil(t, p)
	})

	t.Run("nil config with non-explicit returns nil profile", func(t *testing.T) {
		name, p, err := SelectProfile(nil, "", false)
		require.NoError(t, err)
		require.Empty(t, name)
		require.Nil(t, p)
	})

	t.Run("missing default_profile errors", func(t *testing.T) {
		broken := &Config{DefaultProfile: "ghost"}
		_, _, err := SelectProfile(broken, "", false)
		require.Error(t, err)
	})
}

func TestResolveKeySource(t *testing.T) {
	profileRef := &Profile{KeyRef: "op://<vault>/<item>/<profile-field>"}
	profileFile := &Profile{KeyFile: "/profile/key.pem"}

	tests := []struct {
		name     string
		cliRef   string
		cliFile  string
		envRef   string
		envFile  string
		profile  *Profile
		wantRef  string
		wantFile string
		wantErr  bool
	}{
		{
			name:     "cli file replaces env ref and profile ref",
			cliFile:  "/cli/key.pem",
			envRef:   "op://<vault>/<item>/<env-field>",
			profile:  profileRef,
			wantFile: "/cli/key.pem",
		},
		{
			name:    "cli ref replaces env file and profile file",
			cliRef:  "op://<vault>/<item>/<cli-field>",
			envFile: "/env/key.pem",
			profile: profileFile,
			wantRef: "op://<vault>/<item>/<cli-field>",
		},
		{
			name:     "env file replaces profile ref",
			envFile:  "/env/key.pem",
			profile:  profileRef,
			wantFile: "/env/key.pem",
		},
		{
			name:    "env ref replaces profile file",
			envRef:  "op://<vault>/<item>/<env-field>",
			profile: profileFile,
			wantRef: "op://<vault>/<item>/<env-field>",
		},
		{
			name:    "falls back to profile ref",
			profile: profileRef,
			wantRef: "op://<vault>/<item>/<profile-field>",
		},
		{
			name:     "falls back to profile file",
			profile:  profileFile,
			wantFile: "/profile/key.pem",
		},
		{
			name: "all empty returns empty",
		},
		{
			name:    "cli mutual exclusion rejected",
			cliRef:  "op://<vault>/<item>/<cli-field>",
			cliFile: "/cli/key.pem",
			wantErr: true,
		},
		{
			name:    "env mutual exclusion rejected",
			envRef:  "op://<vault>/<item>/<env-field>",
			envFile: "/env/key.pem",
			wantErr: true,
		},
		{
			name: "profile mutual exclusion rejected",
			profile: &Profile{
				KeyRef:  "op://<vault>/<item>/<profile-field>",
				KeyFile: "/profile/key.pem",
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ref, file, err := ResolveKeySource(tc.cliRef, tc.cliFile, tc.envRef, tc.envFile, tc.profile)
			if tc.wantErr {
				require.Error(t, err)
				if tc.cliRef != "" {
					require.NotContains(t, err.Error(), tc.cliRef)
				}
				if tc.envRef != "" {
					require.NotContains(t, err.Error(), tc.envRef)
				}
				if tc.profile != nil && tc.profile.KeyRef != "" {
					require.NotContains(t, err.Error(), tc.profile.KeyRef)
				}
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.wantRef, ref)
			require.Equal(t, tc.wantFile, file)
		})
	}
}

func TestBuiltinPresetsIsolation(t *testing.T) {
	first := BuiltinPresets()
	require.Equal(t, map[string]string{
		"pull_requests": "write",
		"contents":      "read",
		"metadata":      "read",
	}, first["pr-review"])
	require.Equal(t, map[string]string{
		"issues":        "write",
		"pull_requests": "read",
		"metadata":      "read",
	}, first["pr-comment"])
	require.Equal(t, map[string]string{
		"contents":      "read",
		"pull_requests": "read",
		"issues":        "read",
		"metadata":      "read",
	}, first["read-only"])

	first["pr-review"]["contents"] = "admin"
	second := BuiltinPresets()
	require.Equal(t, "read", second["pr-review"]["contents"])
}

func TestParsePerm(t *testing.T) {
	tests := []struct {
		spec      string
		wantName  string
		wantLevel string
		wantErr   bool
	}{
		{spec: "pull_requests=write", wantName: "pull_requests", wantLevel: "write"},
		{spec: "contents=read", wantName: "contents", wantLevel: "read"},
		{spec: "administration=admin", wantName: "administration", wantLevel: "admin"},
		{spec: "", wantErr: true},
		{spec: "contents", wantErr: true},
		{spec: "=read", wantErr: true},
		{spec: "contents=", wantErr: true},
		{spec: "contents=delete", wantErr: true},
		{spec: "contents=READ", wantErr: true},
		{spec: "Contents=read", wantErr: true},
		{spec: "pull requests=read", wantErr: true},
		{spec: "contents=read=write", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.spec, func(t *testing.T) {
			name, level, err := ParsePerm(tc.spec)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.wantName, name)
			require.Equal(t, tc.wantLevel, level)
		})
	}
}

func TestResolvePermissions(t *testing.T) {
	t.Run("rejects empty preset and perms", func(t *testing.T) {
		_, err := ResolvePermissions(nil, "", nil)
		require.Error(t, err)
	})

	t.Run("rejects unknown preset", func(t *testing.T) {
		_, err := ResolvePermissions(nil, "nonexistent", nil)
		require.Error(t, err)
	})

	t.Run("resolves builtin preset", func(t *testing.T) {
		got, err := ResolvePermissions(nil, "pr-review", nil)
		require.NoError(t, err)
		require.Equal(t, map[string]string{
			"pull_requests": "write",
			"contents":      "read",
			"metadata":      "read",
		}, got)
	})

	t.Run("explicit perm overrides and extends preset", func(t *testing.T) {
		got, err := ResolvePermissions(nil, "pr-review", []string{"pull_requests=read", "checks=write"})
		require.NoError(t, err)
		require.Equal(t, map[string]string{
			"pull_requests": "read",
			"contents":      "read",
			"metadata":      "read",
			"checks":        "write",
		}, got)
	})

	t.Run("profile presets extend and override builtins", func(t *testing.T) {
		profile := &Profile{
			Presets: map[string]map[string]string{
				"pr-review": {
					"pull_requests": "write",
					"metadata":      "read",
				},
				"triage": {
					"issues":   "write",
					"metadata": "read",
				},
			},
		}

		overridden, err := ResolvePermissions(profile, "pr-review", nil)
		require.NoError(t, err)
		require.Equal(t, map[string]string{
			"pull_requests": "write",
			"metadata":      "read",
		}, overridden)

		custom, err := ResolvePermissions(profile, "triage", nil)
		require.NoError(t, err)
		require.Equal(t, map[string]string{
			"issues":   "write",
			"metadata": "read",
		}, custom)

		builtinStillWorks, err := ResolvePermissions(profile, "read-only", nil)
		require.NoError(t, err)
		require.Equal(t, "read", builtinStillWorks["contents"])
	})

	t.Run("enforces profile permission caps", func(t *testing.T) {
		profile := &Profile{
			Permissions: map[string]string{
				"contents":      "read",
				"metadata":      "read",
				"pull_requests": "write",
				"issues":        "admin",
			},
		}

		// Equal or lower levels succeed.
		got, err := ResolvePermissions(profile, "pr-review", []string{"issues=write"})
		require.NoError(t, err)
		require.Equal(t, "write", got["issues"])

		// Missing permission fails.
		_, err = ResolvePermissions(profile, "", []string{"workflows=write"})
		require.Error(t, err)
		require.Contains(t, err.Error(), "workflows")

		// Escalation read -> write fails.
		_, err = ResolvePermissions(profile, "", []string{"contents=write"})
		require.Error(t, err)
		require.Contains(t, err.Error(), "contents=write")

		// Escalation write -> admin fails.
		_, err = ResolvePermissions(profile, "", []string{"pull_requests=admin"})
		require.Error(t, err)
		require.Contains(t, err.Error(), "pull_requests=admin")
	})

	t.Run("skips cap check when profile records no permissions", func(t *testing.T) {
		profile := &Profile{AppID: "12345"}
		got, err := ResolvePermissions(profile, "", []string{"contents=admin"})
		require.NoError(t, err)
		require.Equal(t, map[string]string{"contents": "admin"}, got)
	})
}
