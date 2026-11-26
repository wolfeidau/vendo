package gitrepo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
)

func DetectOrigin(ctx context.Context, dir string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "remote", "get-url", "origin")
	if dir != "" {
		cmd.Dir = dir
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return "", fmt.Errorf("detect git origin remote (%s): %w", msg, err)
		}
		return "", fmt.Errorf("detect git origin remote: %w", err)
	}

	return ParseRemoteURL(stdout.String())
}

func ParseRemoteURL(rawURL string) (string, error) {
	raw := strings.TrimSpace(rawURL)
	if raw == "" {
		return "", errors.New("git remote URL is empty")
	}

	if strings.Contains(raw, "://") {
		return parseSchemeURL(raw)
	}

	return parseSCPURL(raw)
}

func parseSchemeURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse git remote URL: %w", err)
	}

	switch u.Scheme {
	case "https", "ssh":
	default:
		return "", fmt.Errorf("unsupported git remote scheme %q", u.Scheme)
	}

	if u.Hostname() == "" {
		return "", errors.New("git remote URL is missing host")
	}

	if port := u.Port(); port != "" {
		if _, err := strconv.Atoi(port); err != nil {
			return "", fmt.Errorf("invalid git remote port %q: %w", port, err)
		}
	}

	if u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("git remote URL must not contain query or fragment")
	}

	path := normalizeRemotePath(u.Path)
	owner, name, err := ValidateRepo(path)
	if err != nil {
		return "", fmt.Errorf("invalid repository path in git remote URL: %w", err)
	}

	return owner + "/" + name, nil
}

func parseSCPURL(raw string) (string, error) {
	hostPart, pathPart, ok := strings.Cut(raw, ":")
	if !ok {
		return "", errors.New("invalid git remote URL format")
	}

	if err := validateSCPHost(hostPart); err != nil {
		return "", err
	}

	path := normalizeRemotePath(pathPart)
	owner, name, err := ValidateRepo(path)
	if err != nil {
		return "", fmt.Errorf("invalid repository path in git remote URL: %w", err)
	}

	return owner + "/" + name, nil
}

func validateSCPHost(hostPart string) error {
	if hostPart == "" || strings.ContainsAny(hostPart, "/\\ \t\r\n") {
		return errors.New("invalid host in git remote URL")
	}

	if user, host, hasAt := strings.Cut(hostPart, "@"); hasAt {
		if user == "" || host == "" || strings.Contains(host, "@") {
			return errors.New("invalid user or host in git remote URL")
		}
	}

	return nil
}

func normalizeRemotePath(p string) string {
	p = strings.TrimPrefix(p, "/")
	p = strings.TrimSuffix(p, "/")
	p = strings.TrimSuffix(p, ".git")
	return p
}

func ValidateRepo(repo string) (owner, name string, err error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || strings.Contains(name, "/") {
		return "", "", fmt.Errorf("repository %q must be in owner/repo format", repo)
	}

	if !isValidSegment(owner) || !isValidSegment(name) {
		return "", "", fmt.Errorf("repository %q contains invalid owner or repo name", repo)
	}

	return owner, name, nil
}

func isValidSegment(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}

	for i := range len(s) {
		c := s[i]
		isAlpha := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
		isDigit := c >= '0' && c <= '9'
		isPunct := c == '-' || c == '_' || c == '.'
		if !isAlpha && !isDigit && !isPunct {
			return false
		}
	}

	return true
}

func NormalizeRepos(repos []string) (owner string, names []string, err error) {
	if len(repos) == 0 {
		return "", nil, errors.New("at least one repository is required")
	}

	names = make([]string, 0, len(repos))
	for _, repo := range repos {
		o, name, err := ValidateRepo(repo)
		if err != nil {
			return "", nil, err
		}

		if owner == "" {
			owner = o
		} else if !strings.EqualFold(o, owner) {
			return "", nil, fmt.Errorf("all repositories must belong to the same owner: got %q and %q", owner, o)
		}

		names = append(names, name)
	}

	return owner, names, nil
}
