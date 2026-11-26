package commands

import (
	"context"
	"crypto/rsa"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/alecthomas/kong"

	"github.com/wolfeidau/vendo/internal/config"
	"github.com/wolfeidau/vendo/internal/github"
	"github.com/wolfeidau/vendo/internal/gitrepo"
	"github.com/wolfeidau/vendo/internal/jwt"
	"github.com/wolfeidau/vendo/internal/keysource"
)

type CLI struct {
	Version        kong.VersionFlag `name:"version" help:"Print version and exit."`
	Config         string           `name:"config" env:"VENDO_CONFIG" help:"Path to YAML config file (default: ~/.config/vendo/config.yaml)."`
	Profile        string           `name:"profile" env:"VENDO_PROFILE" help:"Config profile name (defaults to default_profile)."`
	LogLevel       string           `name:"log-level" env:"VENDO_LOG_LEVEL" default:"warn" enum:"debug,info,warn,error" help:"Log level: debug, info, warn, or error."`
	LogFormat      string           `name:"log-format" env:"VENDO_LOG_FORMAT" default:"text" enum:"text,json" help:"Log format: text or json."`
	AppID          string           `name:"app-id" help:"GitHub App ID or Client ID used as JWT iss ($VENDO_APP_ID)."`
	KeyRef         string           `name:"key-ref" help:"1Password reference op://<vault>/<item>/<field> ($VENDO_KEY_REF)."`
	KeyFile        string           `name:"key-file" help:"Local PEM file path or - for stdin ($VENDO_KEY_FILE)."`
	InstallationID *int64           `name:"installation-id" help:"Numeric GitHub App installation ID ($VENDO_INSTALLATION_ID)."`
	APIURL         string           `name:"api-url" help:"GitHub REST API base URL ($VENDO_API_URL, default: https://api.github.com)."`
	Timeout        time.Duration    `name:"timeout" env:"VENDO_TIMEOUT" default:"15s" help:"Timeout for op and GitHub API calls."`

	Exec   ExecCmd   `cmd:"" help:"Run a command with a short-lived GH_TOKEN and revoke it on exit."`
	Token  TokenCmd  `cmd:"" help:"Mint a scoped installation access token and print it to stdout."`
	Perms  PermsCmd  `cmd:"" help:"List available permissions and presets for the active profile."`
	Init   InitCmd   `cmd:"" help:"Bootstrap or refresh a config profile from GitHub."`
	Check  CheckCmd  `cmd:"" help:"Validate credentials and installation permissions without minting a token."`
	Revoke RevokeCmd `cmd:"" help:"Revoke an installation access token."`
}

type Globals struct {
	Logger  *slog.Logger
	Version string
	Commit  string
	Date    string
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
	Getenv  func(string) string
	WorkDir string
}

type ExitError struct {
	Code int
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("command exited with status %d", e.Code)
}

func VersionString(version, commit, date string) string {
	if version == "" {
		version = "dev"
	}
	if commit == "" {
		commit = "none"
	}
	if date == "" {
		date = "unknown"
	}
	return fmt.Sprintf("%s (%s, %s)", version, commit, date)
}

func NewParser(cli *CLI, g *Globals, opts ...kong.Option) (*kong.Kong, error) {
	baseOpts := []kong.Option{
		kong.Name("vendo"),
		kong.Description("Mint short-lived, scoped GitHub App installation access tokens."),
		kong.UsageOnError(),
		kong.Vars{
			"version": VersionString(g.Version, g.Commit, g.Date),
		},
		kong.Writers(g.stdout(), g.stderr()),
	}
	baseOpts = append(baseOpts, opts...)
	return kong.New(cli, baseOpts...)
}

func (g *Globals) logger() *slog.Logger {
	if g != nil && g.Logger != nil {
		return g.Logger
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func (g *Globals) stdin() io.Reader {
	if g != nil && g.Stdin != nil {
		return g.Stdin
	}
	return os.Stdin
}

func (g *Globals) stdout() io.Writer {
	if g != nil && g.Stdout != nil {
		return g.Stdout
	}
	return os.Stdout
}

func (g *Globals) stderr() io.Writer {
	if g != nil && g.Stderr != nil {
		return g.Stderr
	}
	return os.Stderr
}

func (g *Globals) getenv(key string) string {
	if g != nil && g.Getenv != nil {
		return g.Getenv(key)
	}
	return os.Getenv(key)
}

func (g *Globals) workDir() string {
	if g != nil {
		return g.WorkDir
	}
	return ""
}

func (g *Globals) userAgent() string {
	version := "dev"
	if g != nil && strings.TrimSpace(g.Version) != "" {
		version = strings.TrimSpace(g.Version)
	}
	return "vendo/" + version
}

type authConfig struct {
	appID   string
	keyRef  string
	keyFile string
}

type installationTarget struct {
	installationID int64
	owner          string
	repo           string
}

func resolveConfigPath(cli *CLI) (path string, explicit bool, err error) {
	if strings.TrimSpace(cli.Config) != "" {
		return cli.Config, true, nil
	}
	p, err := config.DefaultPath()
	if err != nil {
		return "", false, fmt.Errorf("resolve default config path: %w", err)
	}
	return p, false, nil
}

func loadConfigAndProfile(cli *CLI) (*config.Config, string, *config.Profile, error) {
	path, explicit, err := resolveConfigPath(cli)
	if err != nil {
		return nil, "", nil, err
	}
	cfg, err := config.Load(path, explicit)
	if err != nil {
		return nil, "", nil, err
	}
	profileName, profile, err := config.SelectProfile(cfg, cli.Profile, cli.Profile != "")
	if err != nil {
		return nil, "", nil, err
	}
	return cfg, profileName, profile, nil
}

func resolveAPIURL(cli *CLI, g *Globals, profile *config.Profile) string {
	if v := strings.TrimSpace(cli.APIURL); v != "" {
		return v
	}
	if v := strings.TrimSpace(g.getenv("VENDO_API_URL")); v != "" {
		return v
	}
	if profile != nil && strings.TrimSpace(profile.APIURL) != "" {
		return strings.TrimSpace(profile.APIURL)
	}
	return github.DefaultBaseURL
}

func resolveAppID(cli *CLI, g *Globals, profile *config.Profile) (string, error) {
	if v := strings.TrimSpace(cli.AppID); v != "" {
		return v, nil
	}
	if v := strings.TrimSpace(g.getenv("VENDO_APP_ID")); v != "" {
		return v, nil
	}
	if profile != nil && strings.TrimSpace(profile.AppID) != "" {
		return strings.TrimSpace(profile.AppID), nil
	}
	return "", errors.New("missing GitHub App ID: set --app-id, VENDO_APP_ID, or profile app_id")
}

func resolveInstallationID(cli *CLI, g *Globals, profile *config.Profile) (id int64, explicitCLIOrEnv bool, err error) {
	if cli.InstallationID != nil {
		if *cli.InstallationID <= 0 {
			return 0, true, errors.New("--installation-id must be positive")
		}
		return *cli.InstallationID, true, nil
	}
	if raw := strings.TrimSpace(g.getenv("VENDO_INSTALLATION_ID")); raw != "" {
		parsed, parseErr := strconv.ParseInt(raw, 10, 64)
		if parseErr != nil || parsed <= 0 {
			return 0, true, errors.New("VENDO_INSTALLATION_ID must be a positive integer")
		}
		return parsed, true, nil
	}
	if profile != nil && profile.InstallationID > 0 {
		return profile.InstallationID, false, nil
	}
	return 0, false, nil
}

func resolveAuth(cli *CLI, g *Globals, profile *config.Profile, forbidStdinKey bool) (authConfig, error) {
	appID, err := resolveAppID(cli, g, profile)
	if err != nil {
		return authConfig{}, err
	}

	envRef := g.getenv("VENDO_KEY_REF")
	envFile := g.getenv("VENDO_KEY_FILE")
	keyRef, keyFile, err := config.ResolveKeySource(cli.KeyRef, cli.KeyFile, envRef, envFile, profile)
	if err != nil {
		return authConfig{}, err
	}
	if keyRef == "" && keyFile == "" {
		return authConfig{}, errors.New("missing private key source: set --key-ref/--key-file, VENDO_KEY_REF/VENDO_KEY_FILE, or profile key_ref/key_file")
	}
	if keyRef != "" {
		if err := keysource.ValidateOpRef(keyRef); err != nil {
			return authConfig{}, err
		}
	}
	if forbidStdinKey && keyFile == "-" {
		return authConfig{}, errors.New("--key-file - (stdin) is not supported by exec")
	}

	return authConfig{
		appID:   appID,
		keyRef:  keyRef,
		keyFile: keyFile,
	}, nil
}

func newGitHubClient(cli *CLI, g *Globals, profile *config.Profile) (*github.Client, string, error) {
	if cli.Timeout <= 0 {
		return nil, "", errors.New("--timeout must be positive")
	}
	apiURL := resolveAPIURL(cli, g, profile)
	// Leave http.Client.Timeout zero so each request context governs its own deadline.
	client, err := github.NewClient(apiURL, g.userAgent(), &http.Client{})
	if err != nil {
		return nil, "", err
	}
	return client, apiURL, nil
}

func loadPrivateKey(ctx context.Context, g *Globals, auth authConfig) (*rsa.PrivateKey, error) {
	if auth.keyRef != "" {
		return keysource.LoadFromOp(ctx, auth.keyRef)
	}
	return keysource.LoadFromFile(auth.keyFile, g.stdin())
}

func loadKeyAndSignJWT(ctx context.Context, g *Globals, auth authConfig) (string, error) {
	key, err := loadPrivateKey(ctx, g, auth)
	if err != nil {
		return "", err
	}
	return jwt.Sign(auth.appID, key, time.Now())
}

func resolveInstallationTarget(ctx context.Context, g *Globals, repoFlag string, explicitInstID bool, instID int64) (installationTarget, error) {
	if repoFlag != "" {
		owner, name, err := gitrepo.ValidateRepo(repoFlag)
		if err != nil {
			return installationTarget{}, err
		}
		if explicitInstID {
			return installationTarget{installationID: instID}, nil
		}
		return installationTarget{owner: owner, repo: name}, nil
	}

	if instID > 0 {
		return installationTarget{installationID: instID}, nil
	}

	detected, err := gitrepo.DetectOrigin(ctx, g.workDir())
	if err != nil {
		return installationTarget{}, fmt.Errorf("detect repository from git origin (pass -r/--repo or --installation-id): %w", err)
	}
	owner, name, err := gitrepo.ValidateRepo(detected)
	if err != nil {
		return installationTarget{}, err
	}
	return installationTarget{owner: owner, repo: name}, nil
}

func fetchInstallation(ctx context.Context, client *github.Client, appJWT string, target installationTarget) (*github.Installation, error) {
	if target.installationID > 0 {
		return client.GetInstallation(ctx, appJWT, target.installationID)
	}
	return client.GetRepoInstallation(ctx, appJWT, target.owner, target.repo)
}

type scopedTokenRequest struct {
	client         *github.Client
	auth           authConfig
	perms          map[string]string
	installationID int64
	owner          string
	repoNames      []string
}

func prepareScopedTokenRequest(
	ctx context.Context,
	cli *CLI,
	g *Globals,
	profile *config.Profile,
	repos []string,
	allRepos bool,
	permSpecs []string,
	preset string,
	forbidStdinKey bool,
) (*scopedTokenRequest, error) {
	client, _, err := newGitHubClient(cli, g, profile)
	if err != nil {
		return nil, err
	}

	auth, err := resolveAuth(cli, g, profile, forbidStdinKey)
	if err != nil {
		return nil, err
	}

	perms, err := config.ResolvePermissions(profile, preset, permSpecs)
	if err != nil {
		return nil, err
	}

	if allRepos && len(repos) > 0 {
		return nil, errors.New("cannot combine --repo and --all-repos")
	}

	instID, _, err := resolveInstallationID(cli, g, profile)
	if err != nil {
		return nil, err
	}

	if allRepos {
		if instID == 0 {
			return nil, errors.New("--all-repos requires --installation-id (via flag, VENDO_INSTALLATION_ID, or config profile)")
		}
		return &scopedTokenRequest{
			client:         client,
			auth:           auth,
			perms:          perms,
			installationID: instID,
		}, nil
	}

	var owner string
	var repoNames []string
	if len(repos) > 0 {
		owner, repoNames, err = gitrepo.NormalizeRepos(repos)
		if err != nil {
			return nil, err
		}
	} else {
		detectCtx, cancel := context.WithTimeout(ctx, cli.Timeout)
		defer cancel()
		detected, detectErr := gitrepo.DetectOrigin(detectCtx, g.workDir())
		if detectErr != nil {
			return nil, fmt.Errorf("detect repository from git origin (pass -r/--repo or --all-repos): %w", detectErr)
		}
		owner, repoNames, err = gitrepo.NormalizeRepos([]string{detected})
		if err != nil {
			return nil, err
		}
	}

	return &scopedTokenRequest{
		client:         client,
		auth:           auth,
		perms:          perms,
		installationID: instID,
		owner:          owner,
		repoNames:      repoNames,
	}, nil
}

func mintScopedToken(ctx context.Context, cli *CLI, g *Globals, req *scopedTokenRequest) (*github.InstallationToken, error) {
	apiCtx, cancel := context.WithTimeout(ctx, cli.Timeout)
	defer cancel()

	appJWT, err := loadKeyAndSignJWT(apiCtx, g, req.auth)
	if err != nil {
		return nil, err
	}

	instID := req.installationID
	if instID == 0 {
		inst, err := req.client.GetRepoInstallation(apiCtx, appJWT, req.owner, req.repoNames[0])
		if err != nil {
			return nil, err
		}
		instID = inst.ID
	}

	return req.client.CreateInstallationToken(apiCtx, appJWT, instID, github.AccessTokenRequest{
		Repositories: req.repoNames,
		Permissions:  req.perms,
	})
}
