package commands

import (
	"context"
	"fmt"
	"sort"
)

type CheckCmd struct {
	Repo string `short:"r" name:"repo" help:"Target owner/repo for installation lookup when --installation-id is omitted."`
}

func (c *CheckCmd) Run(ctx context.Context, cli *CLI, g *Globals) error {
	_, profileName, profile, err := loadConfigAndProfile(cli)
	if err != nil {
		return err
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

	app, err := client.GetApp(apiCtx, appJWT)
	if err != nil {
		return err
	}

	inst, err := fetchInstallation(apiCtx, client, appJWT, target)
	if err != nil {
		return err
	}

	if _, err := fmt.Fprintf(g.stdout(), "App: %s (ID: %d)\nInstallation ID: %d\nPermissions:\n",
		app.Slug, app.ID, inst.ID); err != nil {
		return err
	}
	if len(inst.Permissions) == 0 {
		if _, err := fmt.Fprintln(g.stdout(), "  (none)"); err != nil {
			return err
		}
	} else {
		for _, name := range sortedKeys(inst.Permissions) {
			if _, err := fmt.Fprintf(g.stdout(), "  %s: %s\n", name, inst.Permissions[name]); err != nil {
				return err
			}
		}
	}

	if profile != nil && len(profile.Permissions) > 0 {
		drifts := computePermissionDrift(profile.Permissions, inst.Permissions)
		if len(drifts) > 0 {
			header := "Warning: permission drift between recorded config profile and live installation"
			if profileName != "" {
				header = fmt.Sprintf("Warning: permission drift between config profile %q and live installation", profileName)
			}
			if _, err := fmt.Fprintf(g.stdout(), "\n%s (run 'vendo init --force' to update):\n", header); err != nil {
				return err
			}
			for _, d := range drifts {
				if _, err := fmt.Fprintf(g.stdout(), "  %s\n", d); err != nil {
					return err
				}
			}
		}
	}

	return nil
}

func computePermissionDrift(recorded, live map[string]string) []string {
	all := make(map[string]struct{}, len(recorded)+len(live))
	for k := range recorded {
		all[k] = struct{}{}
	}
	for k := range live {
		all[k] = struct{}{}
	}

	names := make([]string, 0, len(all))
	for k := range all {
		names = append(names, k)
	}
	sort.Strings(names)

	var drifts []string
	for _, name := range names {
		recVal, inRec := recorded[name]
		liveVal, inLive := live[name]
		switch {
		case inRec && !inLive:
			drifts = append(drifts, fmt.Sprintf("%s: config=%s, live=(removed)", name, recVal))
		case !inRec && inLive:
			drifts = append(drifts, fmt.Sprintf("%s: config=(missing), live=%s", name, liveVal))
		case recVal != liveVal:
			drifts = append(drifts, fmt.Sprintf("%s: config=%s, live=%s", name, recVal, liveVal))
		}
	}
	return drifts
}
