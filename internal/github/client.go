package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	DefaultBaseURL   = "https://api.github.com"
	apiVersion       = "2022-11-28"
	acceptHeader     = "application/vnd.github+json"
	defaultUserAgent = "vendo/dev"
	defaultTimeout   = 15 * time.Second
	maxErrorBodySize = 64 * 1024
)

type operation int

const (
	opApp operation = iota
	opInstallation
	opRepoInstallation
	opCreateToken
	opRevokeToken
)

type Client struct {
	baseURL    string
	userAgent  string
	httpClient *http.Client
}

type App struct {
	ID   int64  `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

type Account struct {
	Login string `json:"login"`
	ID    int64  `json:"id"`
	Type  string `json:"type"`
}

type Installation struct {
	ID                  int64             `json:"id"`
	Account             Account           `json:"account"`
	Permissions         map[string]string `json:"permissions"`
	RepositorySelection string            `json:"repository_selection"`
}

type AccessTokenRequest struct {
	Repositories []string          `json:"repositories,omitempty"`
	Permissions  map[string]string `json:"permissions,omitempty"`
}

type Repository struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	FullName string `json:"full_name"`
}

type InstallationToken struct {
	Token        string            `json:"token"`
	ExpiresAt    time.Time         `json:"expires_at"`
	Permissions  map[string]string `json:"permissions"`
	Repositories []Repository      `json:"repositories"`
}

type APIError struct {
	StatusCode int
	Message    string
	Hint       string
}

func (e *APIError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = http.StatusText(e.StatusCode)
	}
	if e.Hint != "" {
		return fmt.Sprintf("github API error (%d): %s: %s", e.StatusCode, msg, e.Hint)
	}
	return fmt.Sprintf("github API error (%d): %s", e.StatusCode, msg)
}

func NewClient(baseURL, userAgent string, httpClient *http.Client) (*Client, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return nil, fmt.Errorf("failed to parse base URL: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("base URL must include http or https scheme and host")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("base URL must not include query parameters or fragment")
	}

	u.Path = strings.TrimRight(u.Path, "/")

	if strings.TrimSpace(userAgent) == "" {
		userAgent = defaultUserAgent
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultTimeout}
	}

	return &Client{
		baseURL:    u.String(),
		userAgent:  userAgent,
		httpClient: httpClient,
	}, nil
}

func (c *Client) GetApp(ctx context.Context, appJWT string) (*App, error) {
	if strings.TrimSpace(appJWT) == "" {
		return nil, errors.New("app JWT must not be empty")
	}

	var app App
	if err := c.do(ctx, http.MethodGet, "/app", appJWT, nil, http.StatusOK, &app, opApp); err != nil {
		return nil, err
	}
	return &app, nil
}

func (c *Client) GetInstallation(ctx context.Context, appJWT string, installationID int64) (*Installation, error) {
	if strings.TrimSpace(appJWT) == "" {
		return nil, errors.New("app JWT must not be empty")
	}
	if installationID <= 0 {
		return nil, errors.New("installation ID must be positive")
	}

	path := fmt.Sprintf("/app/installations/%d", installationID)
	var inst Installation
	if err := c.do(ctx, http.MethodGet, path, appJWT, nil, http.StatusOK, &inst, opInstallation); err != nil {
		return nil, err
	}
	return &inst, nil
}

func (c *Client) GetRepoInstallation(ctx context.Context, appJWT, owner, repo string) (*Installation, error) {
	if strings.TrimSpace(appJWT) == "" {
		return nil, errors.New("app JWT must not be empty")
	}
	if strings.TrimSpace(owner) == "" || strings.TrimSpace(repo) == "" {
		return nil, errors.New("repository owner and name must not be empty")
	}

	path := fmt.Sprintf("/repos/%s/%s/installation", url.PathEscape(owner), url.PathEscape(repo))
	var inst Installation
	if err := c.do(ctx, http.MethodGet, path, appJWT, nil, http.StatusOK, &inst, opRepoInstallation); err != nil {
		return nil, err
	}
	return &inst, nil
}

func (c *Client) CreateInstallationToken(ctx context.Context, appJWT string, installationID int64, req AccessTokenRequest) (*InstallationToken, error) {
	if strings.TrimSpace(appJWT) == "" {
		return nil, errors.New("app JWT must not be empty")
	}
	if installationID <= 0 {
		return nil, errors.New("installation ID must be positive")
	}

	repos, err := normalizeRepositories(req.Repositories)
	if err != nil {
		return nil, err
	}

	body := AccessTokenRequest{
		Repositories: repos,
		Permissions:  req.Permissions,
	}

	path := fmt.Sprintf("/app/installations/%d/access_tokens", installationID)
	var tok InstallationToken
	if err := c.do(ctx, http.MethodPost, path, appJWT, body, http.StatusCreated, &tok, opCreateToken); err != nil {
		return nil, err
	}
	return &tok, nil
}

func (c *Client) RevokeInstallationToken(ctx context.Context, token string) error {
	if strings.TrimSpace(token) == "" {
		return errors.New("installation token must not be empty")
	}

	return c.do(ctx, http.MethodDelete, "/installation/token", token, nil, http.StatusNoContent, nil, opRevokeToken)
}

func (c *Client) do(ctx context.Context, method, path, bearerToken string, reqBody any, expectedStatus int, out any, op operation) error {
	var bodyReader io.Reader
	if reqBody != nil {
		payload, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("failed to marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(payload)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return fmt.Errorf("failed to build request: %w", err)
	}

	req.Header.Set("Accept", acceptHeader)
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Authorization", "Bearer "+bearerToken)
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("github API request failed: %w", err)
	}
	defer func() {
		// Drain a small remainder so net/http can reuse the connection.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != expectedStatus {
		return parseAPIError(resp, op)
	}

	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("failed to decode github API response: %w", err)
		}
	}

	return nil
}

// GitHub's access_tokens endpoint expects bare repository names without the owner prefix.
func normalizeRepositories(repos []string) ([]string, error) {
	if len(repos) == 0 {
		return nil, nil
	}

	out := make([]string, 0, len(repos))
	for _, r := range repos {
		r = strings.TrimSpace(r)
		if r == "" {
			return nil, errors.New("repository name must not be empty")
		}
		if strings.Contains(r, "/") {
			owner, name, ok := strings.Cut(r, "/")
			if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
				return nil, fmt.Errorf("invalid repository name %q", r)
			}
			r = name
		}
		out = append(out, r)
	}
	return out, nil
}

type errorResponse struct {
	Message string `json:"message"`
}

func parseAPIError(resp *http.Response, op operation) error {
	var errResp errorResponse
	data, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodySize))
	_ = json.Unmarshal(data, &errResp)

	msg := strings.TrimSpace(errResp.Message)
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
	}

	return &APIError{
		StatusCode: resp.StatusCode,
		Message:    msg,
		Hint:       errorHint(resp.StatusCode, op),
	}
}

func errorHint(statusCode int, op operation) string {
	switch statusCode {
	case http.StatusUnauthorized:
		if op == opRevokeToken {
			return "installation token is invalid, expired, or already revoked"
		}
		return "check that app_id matches the private key and system clock is synchronized"
	case http.StatusNotFound:
		switch op {
		case opRepoInstallation:
			return "app is not installed on the target repository or repository does not exist"
		case opInstallation, opCreateToken:
			return "unknown installation ID or app is not installed"
		default:
			return "requested GitHub resource was not found"
		}
	case http.StatusUnprocessableEntity:
		return "requested permissions or repositories may exceed the installation grant"
	default:
		return ""
	}
}
