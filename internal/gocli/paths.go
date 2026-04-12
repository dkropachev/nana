package gocli

import (
	"os"
	"path/filepath"
	"strings"
)

func DefaultUserCodexHome(home string) string {
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return filepath.Join(home, ".nana", "codex-home")
}

func DefaultUserInvestigateCodexHome(home string) string {
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return filepath.Join(home, ".nana", "codex-home-investigate")
}

func CodexHome() string {
	if value := os.Getenv("CODEX_HOME"); value != "" {
		return value
	}
	return DefaultUserCodexHome("")
}

func ResolveCodexHomeForLaunch(cwd string) string {
	if value := strings.TrimSpace(os.Getenv("CODEX_HOME")); value != "" {
		return value
	}
	if scope, _ := resolveDoctorScope(cwd); scope == "project" {
		return filepath.Join(cwd, ".codex")
	}
	return DefaultUserCodexHome(os.Getenv("HOME"))
}

func ResolveInvestigateCodexHome(cwd string) string {
	if scope, _ := resolveDoctorScope(cwd); scope == "project" {
		return filepath.Join(cwd, ".nana", "codex-home-investigate")
	}
	return DefaultUserInvestigateCodexHome(os.Getenv("HOME"))
}

func CodexConfigPath() string {
	return filepath.Join(CodexHome(), "config.toml")
}

func InvestigateCodexConfigPath(cwd string) string {
	return filepath.Join(ResolveInvestigateCodexHome(cwd), "config.toml")
}

func BaseStateDir(cwd string) string {
	if explicit := os.Getenv("NANA_TEAM_STATE_ROOT"); explicit != "" {
		if filepath.IsAbs(explicit) {
			return explicit
		}
		return filepath.Join(cwd, explicit)
	}
	return filepath.Join(cwd, ".nana", "state")
}
