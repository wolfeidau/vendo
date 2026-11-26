package gitrepo

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseRemoteURL(t *testing.T) {
	tests := []struct {
		name    string
		rawURL  string
		want    string
		wantErr bool
	}{
		{
			name:   "scp ssh with git suffix",
			rawURL: "git@github.com:octocat/hello-world.git",
			want:   "octocat/hello-world",
		},
		{
			name:   "scp ssh without git suffix",
			rawURL: "git@github.com:octocat/hello-world",
			want:   "octocat/hello-world",
		},
		{
			name:   "scp ssh custom host alias",
			rawURL: "git@github-work:example-org/service.api.git",
			want:   "example-org/service.api",
		},
		{
			name:   "scp ssh host alias without user",
			rawURL: "github-work:example-org/repo_1.git",
			want:   "example-org/repo_1",
		},
		{
			name:   "ssh scheme without port",
			rawURL: "ssh://git@github.com/octocat/hello-world.git",
			want:   "octocat/hello-world",
		},
		{
			name:   "ssh scheme with port",
			rawURL: "ssh://git@github.com:443/octocat/hello-world.git",
			want:   "octocat/hello-world",
		},
		{
			name:   "ssh scheme without git suffix",
			rawURL: "ssh://git@ssh.github.com:443/octocat/hello-world",
			want:   "octocat/hello-world",
		},
		{
			name:   "https with git suffix",
			rawURL: "https://github.com/octocat/hello-world.git",
			want:   "octocat/hello-world",
		},
		{
			name:   "https without git suffix",
			rawURL: "https://github.com/octocat/hello-world",
			want:   "octocat/hello-world",
		},
		{
			name:   "https with trailing slash",
			rawURL: "https://github.com/octocat/hello-world/",
			want:   "octocat/hello-world",
		},
		{
			name:   "https with git suffix and trailing slash",
			rawURL: "https://github.com/octocat/hello-world.git/",
			want:   "octocat/hello-world",
		},
		{
			name:   "https ghes host with whitespace",
			rawURL: "  https://github.example.com/team/app.git\n",
			want:   "team/app",
		},
		{
			name:    "empty url",
			rawURL:  "",
			wantErr: true,
		},
		{
			name:    "whitespace only",
			rawURL:  "   \n",
			wantErr: true,
		},
		{
			name:    "plain string",
			rawURL:  "not-a-url",
			wantErr: true,
		},
		{
			name:    "local path",
			rawURL:  "/tmp/owner/repo.git",
			wantErr: true,
		},
		{
			name:    "unsupported http scheme",
			rawURL:  "http://github.com/octocat/hello-world.git",
			wantErr: true,
		},
		{
			name:    "unsupported file scheme",
			rawURL:  "file:///tmp/octocat/hello-world.git",
			wantErr: true,
		},
		{
			name:    "https missing host",
			rawURL:  "https:///octocat/hello-world.git",
			wantErr: true,
		},
		{
			name:    "https missing repo",
			rawURL:  "https://github.com/octocat",
			wantErr: true,
		},
		{
			name:    "https extra path segment",
			rawURL:  "https://github.com/octocat/hello-world/pulls",
			wantErr: true,
		},
		{
			name:    "https with query",
			rawURL:  "https://github.com/octocat/hello-world?ref=main",
			wantErr: true,
		},
		{
			name:    "https with fragment",
			rawURL:  "https://github.com/octocat/hello-world#readme",
			wantErr: true,
		},
		{
			name:    "scp missing host",
			rawURL:  "git@:octocat/hello-world.git",
			wantErr: true,
		},
		{
			name:    "scp missing user before at",
			rawURL:  "@github.com:octocat/hello-world.git",
			wantErr: true,
		},
		{
			name:    "scp missing repo",
			rawURL:  "git@github.com:octocat",
			wantErr: true,
		},
		{
			name:    "scp extra path segment",
			rawURL:  "git@github.com:octocat/hello-world/extra.git",
			wantErr: true,
		},
		{
			name:    "scp empty repo after git suffix",
			rawURL:  "git@github.com:octocat/.git",
			wantErr: true,
		},
		{
			name:    "dotdot owner",
			rawURL:  "git@github.com:../hello-world.git",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseRemoteURL(tc.rawURL)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestValidateRepo(t *testing.T) {
	tests := []struct {
		name      string
		repo      string
		wantOwner string
		wantName  string
		wantErr   bool
	}{
		{
			name:      "standard repo",
			repo:      "octocat/hello-world",
			wantOwner: "octocat",
			wantName:  "hello-world",
		},
		{
			name:      "dots and underscores",
			repo:      "my_org/my.repo_v2",
			wantOwner: "my_org",
			wantName:  "my.repo_v2",
		},
		{
			name:    "empty",
			repo:    "",
			wantErr: true,
		},
		{
			name:    "missing slash",
			repo:    "hello-world",
			wantErr: true,
		},
		{
			name:    "missing owner",
			repo:    "/hello-world",
			wantErr: true,
		},
		{
			name:    "missing repo",
			repo:    "octocat/",
			wantErr: true,
		},
		{
			name:    "too many segments",
			repo:    "octocat/hello/world",
			wantErr: true,
		},
		{
			name:    "contains whitespace",
			repo:    "octocat/hello world",
			wantErr: true,
		},
		{
			name:    "dot segment",
			repo:    "./hello-world",
			wantErr: true,
		},
		{
			name:    "dotdot segment",
			repo:    "octocat/..",
			wantErr: true,
		},
		{
			name:    "invalid character",
			repo:    "octocat/hello?world",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			owner, name, err := ValidateRepo(tc.repo)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.wantOwner, owner)
			require.Equal(t, tc.wantName, name)
		})
	}
}

func TestNormalizeRepos(t *testing.T) {
	t.Run("single repo", func(t *testing.T) {
		owner, names, err := NormalizeRepos([]string{"octocat/hello-world"})
		require.NoError(t, err)
		require.Equal(t, "octocat", owner)
		require.Equal(t, []string{"hello-world"}, names)
	})

	t.Run("multiple repos case insensitive owner", func(t *testing.T) {
		owner, names, err := NormalizeRepos([]string{
			"ExampleOrg/repo-one",
			"exampleorg/repo-two",
			"EXAMPLEORG/repo-three",
		})
		require.NoError(t, err)
		require.Equal(t, "ExampleOrg", owner)
		require.Equal(t, []string{"repo-one", "repo-two", "repo-three"}, names)
	})

	t.Run("owner mismatch rejected", func(t *testing.T) {
		_, _, err := NormalizeRepos([]string{"org-a/repo-one", "org-b/repo-two"})
		require.Error(t, err)
		require.Contains(t, err.Error(), "same owner")
	})

	t.Run("empty list rejected", func(t *testing.T) {
		_, _, err := NormalizeRepos(nil)
		require.Error(t, err)
	})

	t.Run("invalid entry rejected", func(t *testing.T) {
		_, _, err := NormalizeRepos([]string{"org-a/repo-one", "bad-repo"})
		require.Error(t, err)
	})
}

func TestDetectOrigin(t *testing.T) {
	ctx := context.Background()

	t.Run("git repo root and subdirectory", func(t *testing.T) {
		dir := t.TempDir()
		runGit(t, dir, "init")
		runGit(t, dir, "remote", "add", "origin", "git@github-work:example-org/sample-repo.git")

		got, err := DetectOrigin(ctx, dir)
		require.NoError(t, err)
		require.Equal(t, "example-org/sample-repo", got)

		subDir := filepath.Join(dir, "nested", "pkg")
		require.NoError(t, os.MkdirAll(subDir, 0o750))

		got, err = DetectOrigin(ctx, subDir)
		require.NoError(t, err)
		require.Equal(t, "example-org/sample-repo", got)
	})

	t.Run("git repo without origin remote", func(t *testing.T) {
		dir := t.TempDir()
		runGit(t, dir, "init")

		_, err := DetectOrigin(ctx, dir)
		require.Error(t, err)
	})

	t.Run("non-git directory", func(t *testing.T) {
		dir := t.TempDir()

		_, err := DetectOrigin(ctx, dir)
		require.Error(t, err)
	})

	t.Run("cancelled context", func(t *testing.T) {
		dir := t.TempDir()
		canceledCtx, cancel := context.WithCancel(ctx)
		cancel()

		_, err := DetectOrigin(canceledCtx, dir)
		require.Error(t, err)
		require.ErrorIs(t, err, context.Canceled)
	})
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v failed: %s", args, string(out))
}
