package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

type RevokeCmd struct {
	TokenStdin bool `name:"token-stdin" help:"Read installation token from stdin (takes precedence over GH_TOKEN)."`
}

func (c *RevokeCmd) Run(ctx context.Context, cli *CLI, g *Globals) error {
	_, _, profile, err := loadConfigAndProfile(cli)
	if err != nil {
		return err
	}

	client, _, err := newGitHubClient(cli, g, profile)
	if err != nil {
		return err
	}

	token, err := readTokenForRevoke(c.TokenStdin, g)
	if err != nil {
		return err
	}

	apiCtx, cancel := context.WithTimeout(ctx, cli.Timeout)
	defer cancel()

	return client.RevokeInstallationToken(apiCtx, token)
}

func readTokenForRevoke(tokenStdin bool, g *Globals) (string, error) {
	if tokenStdin {
		raw, err := io.ReadAll(g.stdin())
		if err != nil {
			return "", fmt.Errorf("read token from stdin: %w", err)
		}
		token := strings.TrimSpace(string(raw))
		clear(raw)
		if token == "" {
			return "", errors.New("no installation token provided on stdin")
		}
		return token, nil
	}

	token := strings.TrimSpace(g.getenv("GH_TOKEN"))
	if token == "" {
		return "", errors.New("no installation token found in GH_TOKEN (or pass --token-stdin)")
	}
	return token, nil
}
