package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const revokeTimeout = 3 * time.Second

type ExecCmd struct {
	Repos    []string `short:"r" name:"repo" help:"Target owner/repo (repeatable; defaults to git origin remote)."`
	AllRepos bool     `name:"all-repos" help:"Opt into all repositories accessible to the installation (requires --installation-id)."`
	Perms    []string `short:"p" name:"perm" help:"Permission pair name=read|write|admin (repeatable)."`
	Preset   string   `name:"preset" help:"Named permission preset (built-in or from config profile)."`
	NoRevoke bool     `name:"no-revoke" help:"Skip revoking the token after the child command exits."`
	Args     []string `arg:"" name:"cmd" passthrough:"partial" help:"Command and arguments to execute (use -- before command flags)."`
}

func (c *ExecCmd) Run(ctx context.Context, cli *CLI, g *Globals) error {
	args := stripLeadingDashDash(c.Args)
	if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
		return errors.New("missing command to execute")
	}

	_, _, profile, err := loadConfigAndProfile(cli)
	if err != nil {
		return err
	}

	req, err := prepareScopedTokenRequest(ctx, cli, g, profile, c.Repos, c.AllRepos, c.Perms, c.Preset, true)
	if err != nil {
		return err
	}

	tok, err := mintScopedToken(ctx, cli, g, req)
	if err != nil {
		return err
	}

	revoke := func() {
		if c.NoRevoke {
			return
		}
		revokeCtx, cancel := context.WithTimeout(context.Background(), revokeTimeout)
		defer cancel()
		if revokeErr := req.client.RevokeInstallationToken(revokeCtx, tok.Token); revokeErr != nil {
			g.logger().WarnContext(revokeCtx, "failed to revoke installation token", "error", revokeErr)
		}
	}

	child := exec.Command(args[0], args[1:]...)
	child.Stdin = g.stdin()
	child.Stdout = g.stdout()
	child.Stderr = g.stderr()
	child.Env = buildChildEnv(os.Environ(), tok.Token)

	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	if err := child.Start(); err != nil {
		revoke()
		return fmt.Errorf("start command %q: %w", args[0], err)
	}

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- child.Wait()
	}()

	var waitErr error
waitLoop:
	for {
		select {
		case waitErr = <-waitCh:
			break waitLoop
		case sig := <-sigCh:
			if child.Process != nil {
				_ = child.Process.Signal(sig)
			}
		}
	}

	revoke()

	if waitErr == nil {
		return nil
	}

	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return &ExitError{Code: 128 + int(status.Signal())}
		}
		return &ExitError{Code: exitErr.ExitCode()}
	}

	return fmt.Errorf("wait for command %q: %w", args[0], waitErr)
}

func stripLeadingDashDash(args []string) []string {
	if len(args) > 0 && args[0] == "--" {
		return args[1:]
	}
	return args
}

func buildChildEnv(environ []string, token string) []string {
	env := make([]string, 0, len(environ)+1)
	for _, entry := range environ {
		key, _, ok := strings.Cut(entry, "=")
		if ok && (key == "GH_TOKEN" || key == "GITHUB_TOKEN") {
			continue
		}
		env = append(env, entry)
	}
	return append(env, "GH_TOKEN="+token)
}
