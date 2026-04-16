package gocli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/dkropachev/nana/internal/gocliassets"
)

type UninstallOptions struct {
	DryRun     bool
	KeepConfig bool
	Verbose    bool
	Purge      bool
	Scope      string
}

type uninstallSummary struct {
	ConfigCleaned          bool
	McpServersRemoved      []string
	AgentEntriesRemoved    int
	TuiSectionRemoved      bool
	TopLevelKeysRemoved    bool
	FeatureFlagsRemoved    bool
	PromptsRemoved         int
	SkillsRemoved          int
	AgentConfigsRemoved    int
	AgentsMdRemoved        bool
	InvestigateHomeRemoved bool
	CacheDirectoryRemoved  bool
}

var nanaMcpServers = []string{"nana_state", "nana_memory", "nana_code_intel", "nana_trace"}

var (
	uninstallTopLevelSettingPattern = regexp.MustCompile(`^\s*(notify|model_reasoning_effort|developer_instructions)\s*=`)
	uninstallFeatureFlagPattern     = regexp.MustCompile(`^\s*(multi_agent|child_agents_md)\s*=`)
)

func Uninstall(repoRoot string, cwd string, args []string) error {
	options, err := parseUninstallArgs(args)
	if err != nil {
		return err
	}

	scope := options.Scope
	if scope == "" {
		if persisted, source := resolveDoctorScope(cwd); source == "persisted" {
			scope = persisted
		} else {
			scope = "user"
		}
	}
	scopeDirs := resolveUninstallScopeDirectories(scope, cwd)

	fmt.Fprintln(os.Stdout, "nana uninstall")
	fmt.Fprintln(os.Stdout, "=====================")
	fmt.Fprintln(os.Stdout)
	if options.DryRun {
		fmt.Fprintln(os.Stdout, "[dry-run mode] No files will be modified.")
		fmt.Fprintln(os.Stdout)
	}
	fmt.Fprintf(os.Stdout, "Resolved scope: %s\n\n", scope)

	summary := uninstallSummary{}

	if options.KeepConfig {
		fmt.Fprintln(os.Stdout, "[1/5] Skipping config.toml cleanup (--keep-config).")
	} else {
		fmt.Fprintln(os.Stdout, "[1/5] Cleaning config.toml...")
		configSummary, err := cleanUninstallConfig(scopeDirs.codexConfigFile, options)
		if err != nil {
			return err
		}
		summary.ConfigCleaned = configSummary.ConfigCleaned
		summary.McpServersRemoved = configSummary.McpServersRemoved
		summary.AgentEntriesRemoved = configSummary.AgentEntriesRemoved
		summary.TuiSectionRemoved = configSummary.TuiSectionRemoved
		summary.TopLevelKeysRemoved = configSummary.TopLevelKeysRemoved
		summary.FeatureFlagsRemoved = configSummary.FeatureFlagsRemoved
	}
	fmt.Fprintln(os.Stdout)

	fmt.Fprintln(os.Stdout, "[2/5] Removing agent prompts...")
	summary.PromptsRemoved, err = removeInstalledPrompts(scopeDirs.promptsDir, filepath.Join(repoRoot, "prompts"), options)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "  %s %d prompt(s).\n\n", conditionalVerb(options.DryRun, "Would remove", "Removed"), summary.PromptsRemoved)

	fmt.Fprintln(os.Stdout, "[3/5] Removing native agent configs...")
	summary.AgentConfigsRemoved, err = removeAgentConfigs(scopeDirs.nativeAgentsDir, options)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "  %s %d agent config(s).\n\n", conditionalVerb(options.DryRun, "Would remove", "Removed"), summary.AgentConfigsRemoved)

	fmt.Fprintln(os.Stdout, "[4/5] Removing skills...")
	summary.SkillsRemoved, err = removeInstalledSkills(scopeDirs.skillsDir, filepath.Join(repoRoot, "skills"), options)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "  %s %d skill(s).\n\n", conditionalVerb(options.DryRun, "Would remove", "Removed"), summary.SkillsRemoved)

	fmt.Fprintln(os.Stdout, "[5/5] Cleaning up...")
	agentsMdPath := filepath.Join(scopeDirs.codexHomeDir, "AGENTS.md")
	if scope == "project" {
		agentsMdPath = filepath.Join(cwd, "AGENTS.md")
	}
	summary.AgentsMdRemoved, err = removeAgentsMd(agentsMdPath, options)
	if err != nil {
		return err
	}
	investigateHome := ResolveInvestigateCodexHome(cwd)
	summary.InvestigateHomeRemoved, err = removeCacheDirectory(investigateHome, options)
	if err != nil {
		return err
	}
	if options.Purge {
		summary.CacheDirectoryRemoved, err = removeCacheDirectory(filepath.Join(cwd, ".nana"), options)
		if err != nil {
			return err
		}
	} else {
		for _, file := range []string{
			filepath.Join(cwd, ".nana", "setup-scope.json"),
			filepath.Join(cwd, ".nana", "hud-config.json"),
		} {
			if fileExists(file) {
				if !options.DryRun {
					_ = os.Remove(file)
				}
				if options.Verbose {
					fmt.Fprintf(os.Stdout, "  %s %s\n", conditionalVerb(options.DryRun, "Would remove", "Removed"), filepath.Base(file))
				}
			}
		}
	}
	fmt.Fprintln(os.Stdout)

	printUninstallSummary(summary, options)
	if options.DryRun {
		fmt.Fprintln(os.Stdout, "\nRun without --dry-run to apply changes.")
	} else {
		fmt.Fprintln(os.Stdout, "\nnana has been uninstalled. Run \"nana setup\" to reinstall.")
	}
	return nil
}

func parseUninstallArgs(args []string) (UninstallOptions, error) {
	options := UninstallOptions{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--dry-run":
			options.DryRun = true
		case "--keep-config":
			options.KeepConfig = true
		case "--verbose":
			options.Verbose = true
		case "--purge":
			options.Purge = true
		case "--scope":
			if i+1 >= len(args) {
				return UninstallOptions{}, fmt.Errorf("missing value after --scope")
			}
			options.Scope = args[i+1]
			i++
		case "--scope=user":
			options.Scope = "user"
		case "--scope=project":
			options.Scope = "project"
		case "--help", "-h":
			return UninstallOptions{}, fmt.Errorf("help requested")
		default:
			if strings.HasPrefix(arg, "-") {
				return UninstallOptions{}, fmt.Errorf("unknown uninstall option: %s", arg)
			}
		}
	}
	return options, nil
}

type uninstallScopeDirectories struct {
	codexConfigFile string
	codexHomeDir    string
	nativeAgentsDir string
	promptsDir      string
	skillsDir       string
}

func resolveUninstallScopeDirectories(scope string, cwd string) uninstallScopeDirectories {
	if scope == "project" {
		codexHomeDir := filepath.Join(cwd, ".codex")
		return uninstallScopeDirectories{
			codexConfigFile: filepath.Join(codexHomeDir, "config.toml"),
			codexHomeDir:    codexHomeDir,
			nativeAgentsDir: filepath.Join(codexHomeDir, "agents"),
			promptsDir:      filepath.Join(codexHomeDir, "prompts"),
			skillsDir:       filepath.Join(codexHomeDir, "skills"),
		}
	}
	return uninstallScopeDirectories{
		codexConfigFile: CodexConfigPath(),
		codexHomeDir:    CodexHome(),
		nativeAgentsDir: filepath.Join(CodexHome(), "agents"),
		promptsDir:      filepath.Join(CodexHome(), "prompts"),
		skillsDir:       filepath.Join(CodexHome(), "skills"),
	}
}

func detectNanaConfigArtifacts(config string) uninstallSummary {
	summary := uninstallSummary{}
	for _, server := range nanaMcpServers {
		if strings.Contains(config, "[mcp_servers."+server+"]") {
			summary.McpServersRemoved = append(summary.McpServersRemoved, server)
		}
	}
	summary.AgentEntriesRemoved = strings.Count(config, "[agents.")
	summary.TuiSectionRemoved = strings.Contains(config, "[tui]") && strings.Contains(config, "nana (NANA) Configuration")
	summary.TopLevelKeysRemoved = regexp.MustCompile(`(?m)^\s*(notify|model_reasoning_effort|developer_instructions)\s*=`).FindString(config) != ""
	summary.FeatureFlagsRemoved = regexp.MustCompile(`(?m)^\s*(multi_agent|child_agents_md)\s*=`).FindString(config) != ""
	return summary
}

func cleanUninstallConfig(configPath string, options UninstallOptions) (uninstallSummary, error) {
	summary := uninstallSummary{}
	content, err := os.ReadFile(configPath)
	if err != nil {
		if options.Verbose {
			fmt.Fprintln(os.Stdout, "  config.toml not found, skipping.")
		}
		return summary, nil
	}
	original := string(content)
	detected := detectNanaConfigArtifacts(original)
	summary.McpServersRemoved = detected.McpServersRemoved
	summary.AgentEntriesRemoved = detected.AgentEntriesRemoved
	summary.TuiSectionRemoved = detected.TuiSectionRemoved
	summary.TopLevelKeysRemoved = detected.TopLevelKeysRemoved
	summary.FeatureFlagsRemoved = detected.FeatureFlagsRemoved

	cleaned := stripNanaConfigContent(original)
	if cleaned != original {
		summary.ConfigCleaned = true
		if !options.DryRun {
			if err := os.WriteFile(configPath, []byte(cleaned), 0o644); err != nil {
				return summary, err
			}
		}
		if options.Verbose {
			fmt.Fprintf(os.Stdout, "  %s %s\n", conditionalVerb(options.DryRun, "Would clean", "Cleaned"), configPath)
		}
	}
	return summary, nil
}

func stripNanaConfigContent(content string) string {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	out := []string{}
	inNanaBlock := false
	inFeatures := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(line, "# nana top-level settings") {
			continue
		}
		if strings.Contains(line, "# nana (NANA) Configuration") {
			inNanaBlock = true
			continue
		}
		if inNanaBlock {
			if strings.Contains(line, "# End nana") {
				inNanaBlock = false
			}
			continue
		}
		if uninstallTopLevelSettingPattern.MatchString(line) {
			continue
		}
		if strings.TrimSpace(line) == "[features]" {
			inFeatures = true
			continue
		}
		if inFeatures {
			if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
				inFeatures = false
			} else if uninstallFeatureFlagPattern.MatchString(line) {
				continue
			} else if trimmed == "" {
				continue
			}
		}
		if !inFeatures || (strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]")) {
			out = append(out, line)
		}
	}
	cleaned := strings.TrimSpace(strings.Join(out, "\n"))
	if cleaned == "" {
		return "\n"
	}
	return cleaned + "\n"
}

func removeInstalledPrompts(promptsDir string, srcPromptsDir string, options UninstallOptions) (int, error) {
	sourceNames := []string{}
	if srcPromptsDir != "" {
		sourceEntries, err := os.ReadDir(srcPromptsDir)
		if err == nil {
			for _, entry := range sourceEntries {
				if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
					continue
				}
				sourceNames = append(sourceNames, entry.Name())
			}
		}
	}
	if len(sourceNames) == 0 {
		embeddedPrompts, err := gocliassets.Prompts()
		if err != nil {
			return 0, err
		}
		for name := range embeddedPrompts {
			if strings.HasSuffix(name, ".md") && !strings.Contains(name, "/") {
				sourceNames = append(sourceNames, name)
			}
		}
	}
	sort.Strings(sourceNames)
	removed := 0
	for _, name := range sourceNames {
		path := filepath.Join(promptsDir, name)
		if !fileExists(path) {
			continue
		}
		if !options.DryRun {
			if err := os.Remove(path); err != nil {
				return removed, err
			}
		}
		if options.Verbose {
			fmt.Fprintf(os.Stdout, "  %s prompt: %s\n", conditionalVerb(options.DryRun, "Would remove", "Removed"), name)
		}
		removed++
	}
	return removed, nil
}

func removeInstalledSkills(skillsDir string, srcSkillsDir string, options UninstallOptions) (int, error) {
	sourceNames := []string{}
	if srcSkillsDir != "" {
		sourceEntries, err := os.ReadDir(srcSkillsDir)
		if err == nil {
			for _, entry := range sourceEntries {
				if entry.IsDir() {
					sourceNames = append(sourceNames, entry.Name())
				}
			}
		}
	}
	if len(sourceNames) == 0 {
		embeddedSkills, err := gocliassets.Skills()
		if err != nil {
			return 0, err
		}
		seen := map[string]bool{}
		for relPath := range embeddedSkills {
			parts := strings.Split(relPath, "/")
			if len(parts) == 0 || parts[0] == "" || seen[parts[0]] {
				continue
			}
			seen[parts[0]] = true
			sourceNames = append(sourceNames, parts[0])
		}
	}
	sort.Strings(sourceNames)
	removed := 0
	for _, name := range sourceNames {
		path := filepath.Join(skillsDir, name)
		if !fileExists(path) {
			continue
		}
		if !options.DryRun {
			if err := os.RemoveAll(path); err != nil {
				return removed, err
			}
		}
		if options.Verbose {
			fmt.Fprintf(os.Stdout, "  %s skill: %s/\n", conditionalVerb(options.DryRun, "Would remove", "Removed"), name)
		}
		removed++
	}
	return removed, nil
}

func removeAgentConfigs(agentsDir string, options UninstallOptions) (int, error) {
	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		return 0, nil
	}
	removed := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".toml") {
			continue
		}
		path := filepath.Join(agentsDir, entry.Name())
		if !options.DryRun {
			if err := os.Remove(path); err != nil {
				return removed, err
			}
		}
		if options.Verbose {
			fmt.Fprintf(os.Stdout, "  %s agent config: %s\n", conditionalVerb(options.DryRun, "Would remove", "Removed"), entry.Name())
		}
		removed++
	}
	if !options.DryRun {
		remaining, _ := os.ReadDir(agentsDir)
		if len(remaining) == 0 {
			_ = os.RemoveAll(agentsDir)
		}
	}
	return removed, nil
}

func removeAgentsMd(path string, options UninstallOptions) (bool, error) {
	if !fileExists(path) {
		return false, nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	text := string(content)
	if !strings.Contains(text, "<!-- nana:generated:agents-md -->") {
		if options.Verbose {
			fmt.Fprintln(os.Stdout, "  AGENTS.md is not NANA-generated, skipping.")
		}
		return false, nil
	}
	if !options.DryRun {
		if err := os.Remove(path); err != nil {
			return false, err
		}
	}
	if options.Verbose {
		fmt.Fprintf(os.Stdout, "  %s AGENTS.md\n", conditionalVerb(options.DryRun, "Would remove", "Removed"))
	}
	return true, nil
}

func removeCacheDirectory(path string, options UninstallOptions) (bool, error) {
	if !fileExists(path) {
		return false, nil
	}
	if !options.DryRun {
		if err := os.RemoveAll(path); err != nil {
			return false, err
		}
	}
	if options.Verbose {
		fmt.Fprintf(os.Stdout, "  %s %s\n", conditionalVerb(options.DryRun, "Would remove", "Removed"), path)
	}
	return true, nil
}

func conditionalVerb(dryRun bool, dry string, live string) string {
	if dryRun {
		return dry
	}
	return live
}

func printUninstallSummary(summary uninstallSummary, options UninstallOptions) {
	prefix := conditionalVerb(options.DryRun, "[dry-run] Would remove", "Removed")
	fmt.Fprintln(os.Stdout, "Uninstall summary:")
	if summary.ConfigCleaned {
		fmt.Fprintf(os.Stdout, "  %s NANA configuration block from config.toml\n", prefix)
		if len(summary.McpServersRemoved) > 0 {
			fmt.Fprintf(os.Stdout, "    MCP servers: %s\n", strings.Join(summary.McpServersRemoved, ", "))
		}
		if summary.AgentEntriesRemoved > 0 {
			fmt.Fprintf(os.Stdout, "    Agent entries: %d\n", summary.AgentEntriesRemoved)
		}
		if summary.TuiSectionRemoved {
			fmt.Fprintln(os.Stdout, "    TUI status line section")
		}
		if summary.TopLevelKeysRemoved {
			fmt.Fprintln(os.Stdout, "    Top-level keys (notify, model_reasoning_effort, developer_instructions)")
		}
		if summary.FeatureFlagsRemoved {
			fmt.Fprintln(os.Stdout, "    Feature flags (multi_agent, child_agents_md)")
		}
	} else if len(summary.McpServersRemoved) == 0 {
		fmt.Fprintln(os.Stdout, "  config.toml: no NANA entries found (or --keep-config used)")
	}
	if summary.PromptsRemoved > 0 {
		fmt.Fprintf(os.Stdout, "  %s %d agent prompt(s)\n", prefix, summary.PromptsRemoved)
	}
	if summary.SkillsRemoved > 0 {
		fmt.Fprintf(os.Stdout, "  %s %d skill(s)\n", prefix, summary.SkillsRemoved)
	}
	if summary.AgentConfigsRemoved > 0 {
		fmt.Fprintf(os.Stdout, "  %s %d native agent config(s)\n", prefix, summary.AgentConfigsRemoved)
	}
	if summary.AgentsMdRemoved {
		fmt.Fprintf(os.Stdout, "  %s AGENTS.md\n", prefix)
	}
	if summary.InvestigateHomeRemoved {
		fmt.Fprintf(os.Stdout, "  %s investigate Codex home\n", prefix)
	}
	if summary.CacheDirectoryRemoved {
		fmt.Fprintf(os.Stdout, "  %s .nana/ cache directory\n", prefix)
	}
	totalActions := 0
	if summary.ConfigCleaned {
		totalActions++
	}
	totalActions += summary.PromptsRemoved + summary.SkillsRemoved + summary.AgentConfigsRemoved
	if summary.AgentsMdRemoved {
		totalActions++
	}
	if summary.InvestigateHomeRemoved {
		totalActions++
	}
	if summary.CacheDirectoryRemoved {
		totalActions++
	}
	if totalActions == 0 {
		fmt.Fprintln(os.Stdout, "  Nothing to remove. nana does not appear to be installed.")
	}
}
