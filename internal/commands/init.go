package commands

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"strings"

	"github.com/wolfeidau/vendo/internal/config"
)

type InitCmd struct {
	Repo       string `short:"r" name:"repo" help:"Target owner/repo for discovering installation ID."`
	SetDefault bool   `name:"set-default" help:"Set this profile as default_profile (automatic for new config files)."`
	Force      bool   `name:"force" help:"Overwrite an existing profile with the same name."`
}

func (c *InitCmd) Run(ctx context.Context, cli *CLI, g *Globals) error {
	configPath, _, err := resolveConfigPath(cli)
	if err != nil {
		return err
	}

	_, statErr := os.Stat(configPath)
	isNewFile := errors.Is(statErr, os.ErrNotExist)
	if statErr != nil && !isNewFile {
		return fmt.Errorf("stat config file: %w", statErr)
	}

	cfg, err := config.Load(configPath, false)
	if err != nil {
		return err
	}

	profileName := strings.TrimSpace(cli.Profile)
	if profileName == "" && cfg.DefaultProfile != "" {
		profileName = cfg.DefaultProfile
	}
	if profileName == "" {
		profileName = "default"
	}

	var baseProfile *config.Profile
	if existing, exists := cfg.Profiles[profileName]; exists {
		if !c.Force {
			return fmt.Errorf("profile %q already exists (use --force to overwrite)", profileName)
		}
		p := existing
		baseProfile = &p
	}

	client, apiURL, err := newGitHubClient(cli, g, baseProfile)
	if err != nil {
		return err
	}

	auth, err := resolveAuth(cli, g, baseProfile, false)
	if err != nil {
		return err
	}

	instID, explicitInstID, err := resolveInstallationID(cli, g, baseProfile)
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

	app, err := client.GetApp(apiCtx, appJWT)
	if err != nil {
		return err
	}

	inst, err := fetchInstallation(apiCtx, client, appJWT, target)
	if err != nil {
		return err
	}

	perms := make(map[string]string, len(inst.Permissions))
	maps.Copy(perms, inst.Permissions)

	updated := config.Profile{
		AppID:          auth.appID,
		KeyRef:         auth.keyRef,
		KeyFile:        auth.keyFile,
		InstallationID: inst.ID,
		APIURL:         apiURL,
		Permissions:    perms,
	}
	if baseProfile != nil && len(baseProfile.Presets) > 0 {
		updated.Presets = baseProfile.Presets
	}

	if cfg.Profiles == nil {
		cfg.Profiles = make(map[string]config.Profile)
	}
	cfg.Profiles[profileName] = updated
	if c.SetDefault || isNewFile || cfg.DefaultProfile == "" {
		cfg.DefaultProfile = profileName
	}

	if err := config.Save(configPath, cfg); err != nil {
		return err
	}

	if _, err := fmt.Fprintf(g.stdout(), "Saved profile %q to %s\nApp: %s (ID: %d)\nInstallation ID: %d\nPermissions:\n",
		profileName, configPath, app.Slug, app.ID, inst.ID); err != nil {
		return err
	}
	if len(perms) == 0 {
		_, err = fmt.Fprintln(g.stdout(), "  (none)")
		return err
	}
	for _, name := range sortedKeys(perms) {
		if _, err := fmt.Fprintf(g.stdout(), "  %s: %s\n", name, perms[name]); err != nil {
			return err
		}
	}
	return nil
}
