package commands

import (
	"context"
	"encoding/json"
	"fmt"
)

type TokenCmd struct {
	Repos    []string `short:"r" name:"repo" help:"Target owner/repo (repeatable; defaults to git origin remote)."`
	AllRepos bool     `name:"all-repos" help:"Opt into all repositories accessible to the installation (requires --installation-id)."`
	Perms    []string `short:"p" name:"perm" help:"Permission pair name=read|write|admin (repeatable)."`
	Preset   string   `name:"preset" help:"Named permission preset (built-in or from config profile)."`
	Format   string   `name:"format" default:"raw" enum:"raw,json" help:"Output format: raw or json."`
}

func (c *TokenCmd) Run(ctx context.Context, cli *CLI, g *Globals) error {
	_, _, profile, err := loadConfigAndProfile(cli)
	if err != nil {
		return err
	}

	req, err := prepareScopedTokenRequest(ctx, cli, g, profile, c.Repos, c.AllRepos, c.Perms, c.Preset, false)
	if err != nil {
		return err
	}

	tok, err := mintScopedToken(ctx, cli, g, req)
	if err != nil {
		return err
	}

	if c.Format == "json" {
		enc := json.NewEncoder(g.stdout())
		enc.SetIndent("", "  ")
		return enc.Encode(tok)
	}

	_, err = fmt.Fprintln(g.stdout(), tok.Token)
	return err
}
