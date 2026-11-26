package config

import (
	"errors"
	"fmt"
	"maps"
	"sort"
	"strings"
)

var permLevelRank = map[string]int{
	"read":  1,
	"write": 2,
	"admin": 3,
}

func BuiltinPresets() map[string]map[string]string {
	return map[string]map[string]string{
		"pr-review": {
			"pull_requests": "write",
			"contents":      "read",
			"metadata":      "read",
		},
		"pr-comment": {
			"issues":        "write",
			"pull_requests": "read",
			"metadata":      "read",
		},
		"read-only": {
			"contents":      "read",
			"pull_requests": "read",
			"issues":        "read",
			"metadata":      "read",
		},
	}
}

func EffectivePresets(profile *Profile) map[string]map[string]string {
	presets := BuiltinPresets()
	if profile == nil {
		return presets
	}

	for name, perms := range profile.Presets {
		copied := make(map[string]string, len(perms))
		maps.Copy(copied, perms)
		presets[name] = copied
	}

	return presets
}

func ParsePerm(spec string) (name, level string, err error) {
	name, level, ok := strings.Cut(spec, "=")
	if !ok {
		return "", "", fmt.Errorf("invalid permission %q: expected name=read|write|admin", spec)
	}

	if !isValidPermName(name) {
		return "", "", fmt.Errorf("invalid permission name %q", name)
	}

	if _, ok := permLevelRank[level]; !ok {
		return "", "", fmt.Errorf("invalid permission level %q for %q: expected read, write, or admin", level, name)
	}

	return name, level, nil
}

func ResolvePermissions(profile *Profile, preset string, perms []string) (map[string]string, error) {
	if preset == "" && len(perms) == 0 {
		return nil, errors.New("at least one permission (--perm or --preset) is required")
	}

	resolved := make(map[string]string)

	if preset != "" {
		presets := EffectivePresets(profile)
		presetPerms, ok := presets[preset]
		if !ok {
			return nil, fmt.Errorf("unknown permission preset %q", preset)
		}
		if len(presetPerms) == 0 {
			return nil, fmt.Errorf("preset %q defines no permissions", preset)
		}
		if err := validatePermMap(presetPerms); err != nil {
			return nil, fmt.Errorf("preset %q: %w", preset, err)
		}
		maps.Copy(resolved, presetPerms)
	}

	for _, spec := range perms {
		name, level, err := ParsePerm(spec)
		if err != nil {
			return nil, err
		}
		resolved[name] = level
	}

	if profile != nil && len(profile.Permissions) > 0 {
		if err := checkPermissionCaps(resolved, profile.Permissions); err != nil {
			return nil, err
		}
	}

	return resolved, nil
}

func checkPermissionCaps(requested, granted map[string]string) error {
	names := make([]string, 0, len(requested))
	for name := range requested {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		reqLevel := requested[name]
		grantedLevel, ok := granted[name]
		if !ok {
			return fmt.Errorf("requested permission %q is not granted by profile", name)
		}

		grantedRank, ok := permLevelRank[grantedLevel]
		if !ok {
			return fmt.Errorf("profile has invalid permission level %q for %q", grantedLevel, name)
		}

		if permLevelRank[reqLevel] > grantedRank {
			return fmt.Errorf("requested permission %s=%s exceeds profile grant %s=%s", name, reqLevel, name, grantedLevel)
		}
	}

	return nil
}

func validatePermMap(perms map[string]string) error {
	names := make([]string, 0, len(perms))
	for name := range perms {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		if !isValidPermName(name) {
			return fmt.Errorf("invalid permission name %q", name)
		}
		level := perms[name]
		if _, ok := permLevelRank[level]; !ok {
			return fmt.Errorf("invalid permission level %q for %q: expected read, write, or admin", level, name)
		}
	}

	return nil
}

func isValidPermName(name string) bool {
	if name == "" {
		return false
	}

	for i := range len(name) {
		c := name[i]
		isLower := c >= 'a' && c <= 'z'
		isDigit := c >= '0' && c <= '9'
		if i == 0 && !isLower {
			return false
		}
		if !isLower && !isDigit && c != '_' && c != '-' {
			return false
		}
	}

	return true
}
