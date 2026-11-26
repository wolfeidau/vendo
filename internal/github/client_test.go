package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNewClientValidation(t *testing.T) {
	t.Parallel()

	invalidURLs := []struct {
		name string
		url  string
	}{
		{name: "empty", url: ""},
		{name: "missing scheme", url: "api.github.com"},
		{name: "missing host", url: "https://"},
		{name: "unsupported scheme", url: "ftp://api.github.com"},
		{name: "malformed", url: "://bad"},
		{name: "query string", url: "https://api.github.com?foo=bar"},
		{name: "fragment", url: "https://api.github.com#frag"},
	}

	for _, tc := range invalidURLs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, err := NewClient(tc.url, "", nil)
			require.Error(t, err)
			require.Nil(t, c)
		})
	}

	t.Run("trims trailing slash and applies defaults", func(t *testing.T) {
		t.Parallel()
		c, err := NewClient("https://ghes.example.com/api/v3///", "", nil)
		require.NoError(t, err)
		require.Equal(t, "https://ghes.example.com/api/v3", c.baseURL)
		require.Equal(t, defaultUserAgent, c.userAgent)
		require.NotNil(t, c.httpClient)
	})
}

func TestGetApp(t *testing.T) {
	t.Parallel()

	const fakeJWT = "header.payload.signature"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "/app", r.URL.Path)
		require.Equal(t, acceptHeader, r.Header.Get("Accept"))
		require.Equal(t, apiVersion, r.Header.Get("X-GitHub-Api-Version"))
		require.Equal(t, "vendo/1.2.3", r.Header.Get("User-Agent"))
		require.Equal(t, "Bearer "+fakeJWT, r.Header.Get("Authorization"))

		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`{"id":42,"slug":"reviewer-bot","name":"Reviewer Bot"}`))
		require.NoError(t, err)
	}))
	t.Cleanup(srv.Close)

	c, err := NewClient(srv.URL+"/", "vendo/1.2.3", srv.Client())
	require.NoError(t, err)

	app, err := c.GetApp(context.Background(), fakeJWT)
	require.NoError(t, err)
	require.Equal(t, &App{ID: 42, Slug: "reviewer-bot", Name: "Reviewer Bot"}, app)
}

func TestGetInstallation(t *testing.T) {
	t.Parallel()

	const fakeJWT = "test-app-jwt"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "/app/installations/98765", r.URL.Path)
		require.Equal(t, "vendo/dev", r.Header.Get("User-Agent"))
		require.Equal(t, "Bearer "+fakeJWT, r.Header.Get("Authorization"))

		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`{
			"id": 98765,
			"account": {"login": "octocat", "id": 1, "type": "User"},
			"permissions": {"contents": "read", "pull_requests": "write"},
			"repository_selection": "selected"
		}`))
		require.NoError(t, err)
	}))
	t.Cleanup(srv.Close)

	c, err := NewClient(srv.URL, "", srv.Client())
	require.NoError(t, err)

	inst, err := c.GetInstallation(context.Background(), fakeJWT, 98765)
	require.NoError(t, err)
	require.Equal(t, &Installation{
		ID: 98765,
		Account: Account{
			Login: "octocat",
			ID:    1,
			Type:  "User",
		},
		Permissions: map[string]string{
			"contents":      "read",
			"pull_requests": "write",
		},
		RepositorySelection: "selected",
	}, inst)
}

func TestGetRepoInstallation(t *testing.T) {
	t.Parallel()

	const fakeJWT = "test-app-jwt"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "/repos/my%20org/my.repo%231/installation", r.URL.EscapedPath())
		require.Equal(t, "Bearer "+fakeJWT, r.Header.Get("Authorization"))

		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`{
			"id": 555,
			"account": {"login": "my org", "id": 10, "type": "Organization"},
			"permissions": {"metadata": "read"},
			"repository_selection": "all"
		}`))
		require.NoError(t, err)
	}))
	t.Cleanup(srv.Close)

	c, err := NewClient(srv.URL, "vendo/test", srv.Client())
	require.NoError(t, err)

	inst, err := c.GetRepoInstallation(context.Background(), fakeJWT, "my org", "my.repo#1")
	require.NoError(t, err)
	require.Equal(t, int64(555), inst.ID)
	require.Equal(t, "all", inst.RepositorySelection)
	require.Equal(t, map[string]string{"metadata": "read"}, inst.Permissions)
}

func TestCreateInstallationToken(t *testing.T) {
	t.Parallel()

	const fakeJWT = "test-app-jwt"
	expiresAt := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

	t.Run("scoped repositories strip owner prefix and serialize permissions", func(t *testing.T) {
		t.Parallel()

		var rawBody map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, http.MethodPost, r.Method)
			require.Equal(t, "/app/installations/1234/access_tokens", r.URL.Path)
			require.Equal(t, "Bearer "+fakeJWT, r.Header.Get("Authorization"))
			require.Equal(t, "application/json", r.Header.Get("Content-Type"))
			require.Equal(t, acceptHeader, r.Header.Get("Accept"))
			require.Equal(t, apiVersion, r.Header.Get("X-GitHub-Api-Version"))

			bodyBytes, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(bodyBytes, &rawBody))

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, err = w.Write([]byte(`{
				"token": "ghs_exampletoken",
				"expires_at": "2026-11-26T12:00:00Z",
				"permissions": {"contents": "read", "pull_requests": "write"},
				"repositories": [
					{"id": 101, "name": "vendo", "full_name": "octocat/vendo"},
					{"id": 102, "name": "demo", "full_name": "octocat/demo"}
				]
			}`))
			require.NoError(t, err)
		}))
		t.Cleanup(srv.Close)

		c, err := NewClient(srv.URL, "vendo/test", srv.Client())
		require.NoError(t, err)

		req := AccessTokenRequest{
			Repositories: []string{"octocat/vendo", "demo"},
			Permissions: map[string]string{
				"contents":      "read",
				"pull_requests": "write",
			},
		}
		tok, err := c.CreateInstallationToken(context.Background(), fakeJWT, 1234, req)
		require.NoError(t, err)

		require.Equal(t, []any{"vendo", "demo"}, rawBody["repositories"])
		require.Equal(t, map[string]any{
			"contents":      "read",
			"pull_requests": "write",
		}, rawBody["permissions"])

		require.Equal(t, &InstallationToken{
			Token:     "ghs_exampletoken",
			ExpiresAt: expiresAt,
			Permissions: map[string]string{
				"contents":      "read",
				"pull_requests": "write",
			},
			Repositories: []Repository{
				{ID: 101, Name: "vendo", FullName: "octocat/vendo"},
				{ID: 102, Name: "demo", FullName: "octocat/demo"},
			},
		}, tok)
	})

	t.Run("all-repos omits repositories key", func(t *testing.T) {
		t.Parallel()

		var rawBody map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			bodyBytes, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(bodyBytes, &rawBody))

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, err = w.Write([]byte(`{
				"token": "ghs_allrepos",
				"expires_at": "2026-11-26T12:00:00Z",
				"permissions": {"metadata": "read"}
			}`))
			require.NoError(t, err)
		}))
		t.Cleanup(srv.Close)

		c, err := NewClient(srv.URL, "vendo/test", srv.Client())
		require.NoError(t, err)

		tok, err := c.CreateInstallationToken(context.Background(), fakeJWT, 1234, AccessTokenRequest{
			Permissions: map[string]string{"metadata": "read"},
		})
		require.NoError(t, err)
		require.Equal(t, "ghs_allrepos", tok.Token)
		_, hasRepos := rawBody["repositories"]
		require.False(t, hasRepos)
		require.Equal(t, map[string]any{"metadata": "read"}, rawBody["permissions"])
	})

	t.Run("rejects malformed repository name upfront", func(t *testing.T) {
		t.Parallel()

		called := false
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			w.WriteHeader(http.StatusCreated)
		}))
		t.Cleanup(srv.Close)

		c, err := NewClient(srv.URL, "vendo/test", srv.Client())
		require.NoError(t, err)

		for _, bad := range [][]string{{""}, {"owner/"}, {"/repo"}, {"a/b/c"}} {
			_, err := c.CreateInstallationToken(context.Background(), fakeJWT, 1234, AccessTokenRequest{
				Repositories: bad,
			})
			require.Error(t, err)
		}
		require.False(t, called)
	})
}

func TestRevokeInstallationToken(t *testing.T) {
	t.Parallel()

	const instToken = "ghs_secret_install_token"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodDelete, r.Method)
		require.Equal(t, "/installation/token", r.URL.Path)
		require.Equal(t, acceptHeader, r.Header.Get("Accept"))
		require.Equal(t, apiVersion, r.Header.Get("X-GitHub-Api-Version"))
		require.Equal(t, "vendo/test", r.Header.Get("User-Agent"))
		require.Equal(t, "Bearer "+instToken, r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	c, err := NewClient(srv.URL, "vendo/test", srv.Client())
	require.NoError(t, err)

	err = c.RevokeInstallationToken(context.Background(), instToken)
	require.NoError(t, err)
}

func TestAPIErrorMapping(t *testing.T) {
	t.Parallel()

	const secretJWT = "eyJhbGciOiJSUzI1NiJ9.supersecretjwt.sig"
	const secretToken = "ghs_super_secret_installation_token"

	tests := []struct {
		name         string
		statusCode   int
		body         string
		call         func(c *Client) error
		wantMsg      string
		wantHintPart string
		noHint       bool
	}{
		{
			name:       "401 on app endpoint",
			statusCode: http.StatusUnauthorized,
			body:       `{"message":"Bad credentials"}`,
			call: func(c *Client) error {
				_, err := c.GetApp(context.Background(), secretJWT)
				return err
			},
			wantMsg:      "Bad credentials",
			wantHintPart: "app_id",
		},
		{
			name:       "401 on revoke endpoint",
			statusCode: http.StatusUnauthorized,
			body:       `{"message":"Bad credentials"}`,
			call: func(c *Client) error {
				return c.RevokeInstallationToken(context.Background(), secretToken)
			},
			wantMsg:      "Bad credentials",
			wantHintPart: "expired",
		},
		{
			name:       "404 on repo installation",
			statusCode: http.StatusNotFound,
			body:       `{"message":"Not Found"}`,
			call: func(c *Client) error {
				_, err := c.GetRepoInstallation(context.Background(), secretJWT, "octocat", "hello-world")
				return err
			},
			wantMsg:      "Not Found",
			wantHintPart: "not installed on the target repository",
		},
		{
			name:       "404 on installation by ID",
			statusCode: http.StatusNotFound,
			body:       `{"message":"Not Found"}`,
			call: func(c *Client) error {
				_, err := c.GetInstallation(context.Background(), secretJWT, 99999)
				return err
			},
			wantMsg:      "Not Found",
			wantHintPart: "unknown installation ID",
		},
		{
			name:       "422 on create token",
			statusCode: http.StatusUnprocessableEntity,
			body:       `{"message":"The permissions requested are not granted to this installation."}`,
			call: func(c *Client) error {
				_, err := c.CreateInstallationToken(context.Background(), secretJWT, 123, AccessTokenRequest{
					Permissions: map[string]string{"administration": "write"},
				})
				return err
			},
			wantMsg:      "The permissions requested are not granted to this installation.",
			wantHintPart: "exceed the installation grant",
		},
		{
			name:       "500 unexpected status with non-JSON body",
			statusCode: http.StatusInternalServerError,
			body:       "upstream failure",
			call: func(c *Client) error {
				_, err := c.GetApp(context.Background(), secretJWT)
				return err
			},
			wantMsg: "Internal Server Error",
			noHint:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.statusCode)
				_, err := w.Write([]byte(tc.body))
				require.NoError(t, err)
			}))
			t.Cleanup(srv.Close)

			c, err := NewClient(srv.URL, "vendo/test", srv.Client())
			require.NoError(t, err)

			err = tc.call(c)
			require.Error(t, err)

			var apiErr *APIError
			require.ErrorAs(t, err, &apiErr)
			require.Equal(t, tc.statusCode, apiErr.StatusCode)
			require.Equal(t, tc.wantMsg, apiErr.Message)
			if tc.noHint {
				require.Empty(t, apiErr.Hint)
			} else {
				require.Contains(t, apiErr.Hint, tc.wantHintPart)
			}

			require.NotContains(t, err.Error(), secretJWT)
			require.NotContains(t, err.Error(), secretToken)
		})
	}
}

func TestInputValidationUpfront(t *testing.T) {
	t.Parallel()

	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	c, err := NewClient(srv.URL, "vendo/test", srv.Client())
	require.NoError(t, err)

	ctx := context.Background()
	_, err = c.GetApp(ctx, "")
	require.Error(t, err)

	_, err = c.GetInstallation(ctx, "", 1)
	require.Error(t, err)
	_, err = c.GetInstallation(ctx, "jwt", 0)
	require.Error(t, err)

	_, err = c.GetRepoInstallation(ctx, "", "owner", "repo")
	require.Error(t, err)
	_, err = c.GetRepoInstallation(ctx, "jwt", "", "repo")
	require.Error(t, err)
	_, err = c.GetRepoInstallation(ctx, "jwt", "owner", "")
	require.Error(t, err)

	_, err = c.CreateInstallationToken(ctx, "", 1, AccessTokenRequest{})
	require.Error(t, err)
	_, err = c.CreateInstallationToken(ctx, "jwt", -5, AccessTokenRequest{})
	require.Error(t, err)

	err = c.RevokeInstallationToken(ctx, "")
	require.Error(t, err)

	require.False(t, called)
}

func TestContextCancellation(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	c, err := NewClient(srv.URL, "vendo/test", srv.Client())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = c.GetApp(ctx, "test-jwt")
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)
}
