package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/wolfeidau/vendo/internal/config"
	"github.com/wolfeidau/vendo/internal/gitrepo"
)

type PermsCmd struct {
	Live   bool   `name:"live" help:"Query GitHub API for live installation permissions."`
	Repo   string `short:"r" name:"repo" help:"Target owner/repo for installation lookup with --live."`
	Format string `name:"format" default:"text" enum:"text,json" help:"Output format: text or json."`
}

type permsOutput struct {
	Source         string                       `json:"source"`
	Profile        string                       `json:"profile,omitempty"`
	InstallationID int64                        `json:"installation_id,omitempty"`
	Permissions    map[string]string            `json:"permissions"`
	Presets        map[string]map[string]string `json:"presets"`
}

func (c *PermsCmd) Run(ctx context.Context, cli *CLI, g *Globals) error {
	_, profileName, profile, err := loadConfigAndProfile(cli)
	if err != nil {
		return err
	}

	if c.Repo != "" {
		if _, _, err := gitrepo.ValidateRepo(c.Repo); err != nil {
			return err
		}
	}

	presets := config.EffectivePresets(profile)

	if !c.Live {
		if profile == nil || len(profile.Permissions) == 0 {
			return errors.New("no permissions recorded in config profile; run 'vendo init' to record them or 'vendo perms --live' to query GitHub")
		}
		out := permsOutput{
			Source:         "config",
			Profile:        profileName,
			InstallationID: profile.InstallationID,
			Permissions:    profile.Permissions,
			Presets:        presets,
		}
		return writePermsOutput(g.stdout(), c.Format, out)
	}

	client, _, err := newGitHubClient(cli, g, profile)
	if err != nil {
		return err
	}

	auth, err := resolveAuth(cli, g, profile, false)
	if err != nil {
		return err
	}

	instID, explicitInstID, err := resolveInstallationID(cli, g, profile)
	if err != nil {
		return err
	}

	apiCtx, cancel := context.WithTimeout(ctx, cli.Timeout)
	defer cancel()

	target, err := resolveInstallationTarget(apiCtx, g, c.Repo, explicitInstID, instID)
	if err != nil {
		return err
	}

	appJWT, err := loadKeyAndSignJWT(apiCtx, g, auth)
	if err != nil {
		return err
	}

	inst, err := fetchInstallation(apiCtx, client, appJWT, target)
	if err != nil {
		return err
	}

	out := permsOutput{
		Source:         "live",
		Profile:        profileName,
		InstallationID: inst.ID,
		Permissions:    inst.Permissions,
		Presets:        presets,
	}
	return writePermsOutput(g.stdout(), c.Format, out)
}

func writePermsOutput(w io.Writer, format string, out permsOutput) error {
	if out.Permissions == nil {
		out.Permissions = map[string]string{}
	}
	if format == "json" {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	header := fmt.Sprintf("Permissions (%s", out.Source)
	if out.Profile != "" {
		header += ", profile: " + out.Profile
	}
	if out.InstallationID > 0 {
		header += fmt.Sprintf(", installation: %d", out.InstallationID)
	}
	header += "):"

	if _, err := fmt.Fprintln(w, header); err != nil {
		return err
	}

	if len(out.Permissions) == 0 {
		if _, err := fmt.Fprintln(w, "  (none)"); err != nil {
			return err
		}
	} else {
		for _, name := range sortedKeys(out.Permissions) {
			if _, err := fmt.Fprintf(w, "  %s: %s\n", name, out.Permissions[name]); err != nil {
				return err
			}
		}
	}

	if _, err := fmt.Fprintln(w, "\nPresets:"); err != nil {
		return err
	}
	for _, presetName := range sortedKeys(out.Presets) {
		perms := out.Presets[presetName]
		pairs := make([]string, 0, len(perms))
		for _, k := range sortedKeys(perms) {
			pairs = append(pairs, k+"="+perms[k])
		}
		if _, err := fmt.Fprintf(w, "  %s: %s\n", presetName, strings.Join(pairs, ", ")); err != nil {
			return err
		}
	}

	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
