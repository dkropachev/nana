package testenv

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

var blockedEnvPrefixes = []string{
	"ANTHROPIC_",
	"AZURE_OPENAI_",
	"CODEX_",
	"GH_",
	"GITHUB_",
	"GIT_AUTHOR_",
	"GIT_COMMITTER_",
	"NANA_",
	"OPENAI_",
	"XDG_",
}

var blockedEnvKeys = map[string]bool{
	"APPDATA":           true,
	"EMAIL":             true,
	"GIT_ASKPASS":       true,
	"GIT_CONFIG_GLOBAL": true,
	"GIT_CONFIG_SYSTEM": true,
	"HOME":              true,
	"LOCALAPPDATA":      true,
	"USERPROFILE":       true,
}

func Activate(namespace string) (func(), error) {
	root, err := os.MkdirTemp("", "nana-testenv-"+sanitizeNamespace(namespace)+"-")
	if err != nil {
		return nil, err
	}

	entries, err := sanitizedEntries(os.Environ(), root)
	if err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}

	os.Clearenv()
	for _, entry := range entries {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			_ = os.RemoveAll(root)
			return nil, err
		}
	}

	return func() {
		_ = os.RemoveAll(root)
	}, nil
}

func sanitizedEntries(base []string, root string) ([]string, error) {
	baseMap := envMapFromEntries(base)
	originalHome := firstNonEmpty(baseMap["HOME"], baseMap["USERPROFILE"])
	goPath := firstNonEmpty(baseMap["GOPATH"], defaultGoPath(originalHome))
	goModCache := firstNonEmpty(baseMap["GOMODCACHE"], defaultGoModCache(goPath))
	goCache := firstNonEmpty(baseMap["GOCACHE"], defaultGoCache(baseMap, originalHome))

	home := filepath.Join(root, "home")
	xdgConfig := filepath.Join(root, "xdg", "config")
	xdgState := filepath.Join(root, "xdg", "state")
	xdgCache := filepath.Join(root, "xdg", "cache")
	codexHome := filepath.Join(home, ".nana", "codex-home")
	appData := filepath.Join(root, "appdata")
	localAppData := filepath.Join(root, "localappdata")

	for _, dir := range []string{home, xdgConfig, xdgState, xdgCache, codexHome, appData, localAppData} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}

	envMap := map[string]string{}
	for key, value := range baseMap {
		if blockedEnvKeys[key] || hasBlockedPrefix(key) {
			continue
		}
		envMap[key] = value
	}

	envMap["APPDATA"] = appData
	envMap["CODEX_HOME"] = codexHome
	envMap["GIT_CONFIG_NOSYSTEM"] = "1"
	if goCache != "" {
		envMap["GOCACHE"] = goCache
	}
	if goModCache != "" {
		envMap["GOMODCACHE"] = goModCache
	}
	if goPath != "" {
		envMap["GOPATH"] = goPath
	}
	envMap["HOME"] = home
	envMap["LOCALAPPDATA"] = localAppData
	envMap["USERPROFILE"] = home
	envMap["XDG_CACHE_HOME"] = xdgCache
	envMap["XDG_CONFIG_HOME"] = xdgConfig
	envMap["XDG_STATE_HOME"] = xdgState

	if runtime.GOOS == "windows" {
		volume := filepath.VolumeName(home)
		if volume != "" {
			envMap["HOMEDRIVE"] = volume
			envMap["HOMEPATH"] = strings.TrimPrefix(home, volume)
		}
	}

	keys := make([]string, 0, len(envMap))
	for key := range envMap {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	entries := make([]string, 0, len(keys))
	for _, key := range keys {
		entries = append(entries, key+"="+envMap[key])
	}
	return entries, nil
}

func envMapFromEntries(entries []string) map[string]string {
	envMap := map[string]string{}
	for _, entry := range entries {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		envMap[key] = value
	}
	return envMap
}

func hasBlockedPrefix(key string) bool {
	for _, prefix := range blockedEnvPrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

func defaultGoCache(baseMap map[string]string, home string) string {
	switch runtime.GOOS {
	case "windows":
		if localAppData := strings.TrimSpace(baseMap["LOCALAPPDATA"]); localAppData != "" {
			return filepath.Join(localAppData, "go-build")
		}
	case "darwin":
		if home != "" {
			return filepath.Join(home, "Library", "Caches", "go-build")
		}
	default:
		if xdgCache := strings.TrimSpace(baseMap["XDG_CACHE_HOME"]); xdgCache != "" {
			return filepath.Join(xdgCache, "go-build")
		}
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".cache", "go-build")
}

func defaultGoModCache(goPath string) string {
	if goPath == "" {
		return ""
	}
	return filepath.Join(goPath, "pkg", "mod")
}

func defaultGoPath(home string) string {
	if home == "" {
		return ""
	}
	return filepath.Join(home, "go")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func sanitizeNamespace(namespace string) string {
	replacer := strings.NewReplacer("/", "-", "\\", "-", " ", "-")
	sanitized := replacer.Replace(strings.TrimSpace(namespace))
	if sanitized == "" {
		return "default"
	}
	return sanitized
}
