package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"go.yaml.in/yaml/v3"
)

type Config struct {
	DefaultProfile string             `yaml:"default_profile,omitempty"`
	Profiles       map[string]Profile `yaml:"profiles,omitempty"`
}

type Profile struct {
	AppID          string                       `yaml:"app_id,omitempty"`
	KeyRef         string                       `yaml:"key_ref,omitempty"`
	KeyFile        string                       `yaml:"key_file,omitempty"`
	InstallationID int64                        `yaml:"installation_id,omitempty"`
	APIURL         string                       `yaml:"api_url,omitempty"`
	Permissions    map[string]string            `yaml:"permissions,omitempty"`
	Presets        map[string]map[string]string `yaml:"presets,omitempty"`
}

func DefaultPath() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "vendo", "config.yaml"), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}

	return filepath.Join(home, ".config", "vendo", "config.yaml"), nil
}

func Load(path string, explicit bool) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && !explicit {
			return &Config{}, nil
		}
		return nil, fmt.Errorf("read config file: %w", err)
	}

	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parse config file: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func (c *Config) Validate() error {
	if c == nil {
		return errors.New("config is nil")
	}

	names := make([]string, 0, len(c.Profiles))
	for name := range c.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		if name == "" {
			return errors.New("profile name must not be empty")
		}
		if err := validateProfile(name, c.Profiles[name]); err != nil {
			return err
		}
	}

	return nil
}

func validateProfile(name string, p Profile) error {
	if p.KeyRef != "" && p.KeyFile != "" {
		return fmt.Errorf("profile %q cannot set both key_ref and key_file", name)
	}

	if p.InstallationID < 0 {
		return fmt.Errorf("profile %q has negative installation_id", name)
	}

	if err := validatePermMap(p.Permissions); err != nil {
		return fmt.Errorf("profile %q permissions: %w", name, err)
	}

	presetNames := make([]string, 0, len(p.Presets))
	for presetName := range p.Presets {
		presetNames = append(presetNames, presetName)
	}
	sort.Strings(presetNames)

	for _, presetName := range presetNames {
		if presetName == "" {
			return fmt.Errorf("profile %q has an empty preset name", name)
		}
		perms := p.Presets[presetName]
		if len(perms) == 0 {
			return fmt.Errorf("profile %q preset %q must define at least one permission", name, presetName)
		}
		if err := validatePermMap(perms); err != nil {
			return fmt.Errorf("profile %q preset %q: %w", name, presetName, err)
		}
	}

	return nil
}

func Save(path string, cfg *Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("encode config file: %w", err)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".config-*.yaml.tmp")
	if err != nil {
		return fmt.Errorf("create temp config file: %w", err)
	}
	tmpPath := tmp.Name()

	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set config file permissions: %w", err)
	}

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp config file: %w", err)
	}

	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp config file: %w", err)
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp config file: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace config file: %w", err)
	}
	committed = true

	return nil
}

func SelectProfile(cfg *Config, name string, explicit bool) (string, *Profile, error) {
	if name != "" {
		if cfg != nil {
			if p, ok := cfg.Profiles[name]; ok {
				return name, &p, nil
			}
		}
		if explicit {
			return "", nil, fmt.Errorf("profile %q not found", name)
		}
		return "", nil, nil
	}

	if explicit {
		return "", nil, errors.New("profile name must not be empty")
	}

	if cfg == nil || cfg.DefaultProfile == "" {
		return "", nil, nil
	}

	p, ok := cfg.Profiles[cfg.DefaultProfile]
	if !ok {
		return "", nil, fmt.Errorf("default profile %q not found", cfg.DefaultProfile)
	}

	return cfg.DefaultProfile, &p, nil
}

func ResolveKeySource(cliRef, cliFile, envRef, envFile string, profile *Profile) (keyRef, keyFile string, err error) {
	if cliRef != "" && cliFile != "" {
		return "", "", errors.New("cannot specify both --key-ref and --key-file")
	}

	if envRef != "" && envFile != "" {
		return "", "", errors.New("cannot set both VENDO_KEY_REF and VENDO_KEY_FILE")
	}

	if profile != nil && profile.KeyRef != "" && profile.KeyFile != "" {
		return "", "", errors.New("profile cannot set both key_ref and key_file")
	}

	if cliRef != "" || cliFile != "" {
		return cliRef, cliFile, nil
	}

	if envRef != "" || envFile != "" {
		return envRef, envFile, nil
	}

	if profile != nil && (profile.KeyRef != "" || profile.KeyFile != "") {
		return profile.KeyRef, profile.KeyFile, nil
	}

	return "", "", nil
}
