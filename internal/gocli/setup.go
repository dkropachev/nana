package gocli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dkropachev/nana/internal/gocliassets"
)

type SetupOptions struct {
	Scope   string
	DryRun  bool
	Force   bool
	Verbose bool
}

func Setup(repoRoot string, cwd string, args []string) error {
	options, persistedSource, err := parseSetupArgs(cwd, args)
	if err != nil {
		return err
	}

	fmt.Fprintln(os.Stdout, "nana setup")
	fmt.Fprintln(os.Stdout, "=====================")
	fmt.Fprintln(os.Stdout)
	if options.DryRun {
		fmt.Fprintln(os.Stdout, "[dry-run mode] No files will be modified.")
		fmt.Fprintln(os.Stdout)
	}
	if persistedSource {
		fmt.Fprintf(os.Stdout, "Using setup scope: %s (from .nana/setup-scope.json)\n", options.Scope)
	} else {
		fmt.Fprintf(os.Stdout, "Using setup scope: %s\n", options.Scope)
	}
	if options.Force {
		fmt.Fprintln(os.Stdout, "Force mode: enabled additional destructive maintenance")
	}
	fmt.Fprintln(os.Stdout)

	scopeDirs := resolveSetupScopeDirectories(cwd, options.Scope)
	if options.Scope == "user" {
		fmt.Fprintln(os.Stdout, "User scope leaves project AGENTS.md unchanged.")
	}

	if err := installPrompts(repoRoot, scopeDirs.promptsDir, options); err != nil {
		return err
	}
	if err := installSkills(repoRoot, scopeDirs.skillsDir, options); err != nil {
		return err
	}
	if err := installAgents(scopeDirs.nativeAgentsDir, options); err != nil {
		return err
	}
	if err := ensureNanaDirectories(cwd, options); err != nil {
		return err
	}
	if err := writeSetupConfig(scopeDirs.codexConfigFile, options); err != nil {
		return err
	}
	if err := writeSetupAgentsMd(repoRoot, cwd, scopeDirs.codexHomeDir, options); err != nil {
		return err
	}
	if err := installInvestigateCodexHome(repoRoot, cwd, options.Scope, options, scopeDirs.codexHomeDir); err != nil {
		return err
	}
	if !options.DryRun {
		if err := os.MkdirAll(filepath.Join(cwd, ".nana"), 0o755); err != nil {
			return err
		}
		scopePath := filepath.Join(cwd, ".nana", "setup-scope.json")
		payload, _ := json.Marshal(map[string]string{"scope": options.Scope})
		if err := os.WriteFile(scopePath, payload, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func parseSetupArgs(cwd string, args []string) (SetupOptions, bool, error) {
	options := SetupOptions{Scope: "", DryRun: false, Force: false, Verbose: false}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--help", "-h":
			return options, false, fmt.Errorf("help requested")
		case "--dry-run":
			options.DryRun = true
		case "--force":
			options.Force = true
		case "--verbose":
			options.Verbose = true
		case "--scope":
			if i+1 >= len(args) {
				return options, false, fmt.Errorf("missing value after --scope")
			}
			options.Scope = args[i+1]
			i++
		case "--scope=user":
			options.Scope = "user"
		case "--scope=project":
			options.Scope = "project"
		default:
			if strings.HasPrefix(arg, "-") {
				return options, false, fmt.Errorf("unknown setup option: %s", arg)
			}
		}
	}
	persistedSource := false
	if options.Scope == "" {
		scope, source := resolveDoctorScope(cwd)
		options.Scope = scope
		persistedSource = source == "persisted"
	}
	if options.Scope != "user" && options.Scope != "project" {
		return options, false, fmt.Errorf("invalid scope: %s", options.Scope)
	}
	return options, persistedSource, nil
}

type setupScopeDirectories struct {
	codexConfigFile string
	codexHomeDir    string
	nativeAgentsDir string
	promptsDir      string
	skillsDir       string
}

func resolveSetupScopeDirectories(cwd string, scope string) setupScopeDirectories {
	if scope == "project" {
		codexHomeDir := filepath.Join(cwd, ".codex")
		return setupScopeDirectories{
			codexConfigFile: filepath.Join(codexHomeDir, "config.toml"),
			codexHomeDir:    codexHomeDir,
			nativeAgentsDir: filepath.Join(codexHomeDir, "agents"),
			promptsDir:      filepath.Join(codexHomeDir, "prompts"),
			skillsDir:       filepath.Join(codexHomeDir, "skills"),
		}
	}
	return setupScopeDirectories{
		codexConfigFile: filepath.Join(CodexHome(), "config.toml"),
		codexHomeDir:    CodexHome(),
		nativeAgentsDir: filepath.Join(CodexHome(), "agents"),
		promptsDir:      filepath.Join(CodexHome(), "prompts"),
		skillsDir:       filepath.Join(CodexHome(), "skills"),
	}
}

func resolveInvestigateScopeDirectories(cwd string, scope string) setupScopeDirectories {
	if scope == "project" {
		codexHomeDir := filepath.Join(cwd, ".nana", "codex-home-investigate")
		return setupScopeDirectories{
			codexConfigFile: filepath.Join(codexHomeDir, "config.toml"),
			codexHomeDir:    codexHomeDir,
			nativeAgentsDir: filepath.Join(codexHomeDir, "agents"),
			promptsDir:      filepath.Join(codexHomeDir, "prompts"),
			skillsDir:       filepath.Join(codexHomeDir, "skills"),
		}
	}
	codexHomeDir := DefaultUserInvestigateCodexHome(os.Getenv("HOME"))
	return setupScopeDirectories{
		codexConfigFile: filepath.Join(codexHomeDir, "config.toml"),
		codexHomeDir:    codexHomeDir,
		nativeAgentsDir: filepath.Join(codexHomeDir, "agents"),
		promptsDir:      filepath.Join(codexHomeDir, "prompts"),
		skillsDir:       filepath.Join(codexHomeDir, "skills"),
	}
}

func installInvestigateCodexHome(repoRoot string, cwd string, scope string, options SetupOptions, sourceCodexHome string) error {
	investigateDirs := resolveInvestigateScopeDirectories(cwd, scope)
	if err := installPrompts(repoRoot, investigateDirs.promptsDir, options); err != nil {
		return err
	}
	if err := installSkills(repoRoot, investigateDirs.skillsDir, options); err != nil {
		return err
	}
	if err := installAgents(investigateDirs.nativeAgentsDir, options); err != nil {
		return err
	}
	if err := writeSetupConfig(investigateDirs.codexConfigFile, options); err != nil {
		return err
	}
	if err := writeSetupAgentsMd(repoRoot, cwd, investigateDirs.codexHomeDir, options); err != nil {
		return err
	}
	return bootstrapInvestigateAuth(sourceCodexHome, investigateDirs.codexHomeDir, options)
}

func bootstrapInvestigateAuth(sourceCodexHome string, investigateCodexHome string, options SetupOptions) error {
	source := filepath.Join(sourceCodexHome, "auth.json")
	target := filepath.Join(investigateCodexHome, "auth.json")
	if _, err := os.Stat(target); err == nil {
		return nil
	}
	content, err := os.ReadFile(source)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if options.DryRun {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	return os.WriteFile(target, content, 0o644)
}

func installPrompts(repoRoot string, promptsDir string, options SetupOptions) error {
	srcDir := filepath.Join(repoRoot, "prompts")
	entries, err := os.ReadDir(srcDir)
	if err == nil {
		if !options.DryRun {
			if err := os.MkdirAll(promptsDir, 0o755); err != nil {
				return err
			}
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			src := filepath.Join(srcDir, entry.Name())
			dst := filepath.Join(promptsDir, entry.Name())
			if err := copyFileIfChanged(src, dst, options); err != nil {
				return err
			}
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	embeddedPrompts, err := gocliassets.Prompts()
	if err != nil {
		return err
	}
	if !options.DryRun {
		if err := os.MkdirAll(promptsDir, 0o755); err != nil {
			return err
		}
	}
	names := make([]string, 0, len(embeddedPrompts))
	for name := range embeddedPrompts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		if err := writeBytesIfChanged(filepath.Join(promptsDir, name), []byte(embeddedPrompts[name]), options); err != nil {
			return err
		}
	}
	return nil
}

func installSkills(repoRoot string, skillsDir string, options SetupOptions) error {
	srcDir := filepath.Join(repoRoot, "skills")
	entries, err := os.ReadDir(srcDir)
	if err == nil {
		if !options.DryRun {
			if err := os.MkdirAll(skillsDir, 0o755); err != nil {
				return err
			}
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			srcPath := filepath.Join(srcDir, entry.Name())
			dstPath := filepath.Join(skillsDir, entry.Name())
			if err := copyDirIfChanged(srcPath, dstPath, options); err != nil {
				return err
			}
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	embeddedSkills, err := gocliassets.Skills()
	if err != nil {
		return err
	}
	if !options.DryRun {
		if err := os.MkdirAll(skillsDir, 0o755); err != nil {
			return err
		}
	}
	paths := make([]string, 0, len(embeddedSkills))
	for relPath := range embeddedSkills {
		paths = append(paths, relPath)
	}
	sort.Strings(paths)
	for _, relPath := range paths {
		if err := writeBytesIfChanged(filepath.Join(skillsDir, relPath), []byte(embeddedSkills[relPath]), options); err != nil {
			return err
		}
	}
	return nil
}

func installAgents(agentsDir string, options SetupOptions) error {
	if !options.DryRun {
		if err := os.MkdirAll(agentsDir, 0o755); err != nil {
			return err
		}
	}
	executor := strings.Join([]string{
		`name = "executor"`,
		`description = "Code implementation"`,
		`developer_instructions = """`,
		`Execute the requested code changes and verify them.`,
		`"""`,
		"",
	}, "\n")
	return writeFileIfChanged(filepath.Join(agentsDir, "executor.toml"), executor, options)
}

func ensureNanaDirectories(cwd string, options SetupOptions) error {
	for _, dir := range []string{
		filepath.Join(cwd, ".nana", "state"),
		filepath.Join(cwd, ".nana", "plans"),
		filepath.Join(cwd, ".nana", "logs"),
	} {
		if options.DryRun {
			continue
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func writeSetupConfig(configPath string, options SetupOptions) error {
	content := strings.Join([]string{
		fmt.Sprintf(`model_reasoning_effort = "%s"`, defaultNanaReasoningMode()),
		"",
		"[agents]",
		"max_threads = 6",
		"max_depth = 2",
		"",
		"[env]",
		`USE_NANA_EXPLORE_CMD = "1"`,
		"",
	}, "\n")
	return writeFileIfChanged(configPath, content, options)
}

func writeSetupAgentsMd(repoRoot string, cwd string, codexHomeDir string, options SetupOptions) error {
	templatePath := filepath.Join(repoRoot, "templates", "AGENTS.md")
	templateBytes, err := os.ReadFile(templatePath)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		templates, embeddedErr := gocliassets.Templates()
		if embeddedErr != nil {
			return embeddedErr
		}
		content, ok := templates["AGENTS.md"]
		if !ok {
			return fmt.Errorf("embedded AGENTS template missing")
		}
		templateBytes = []byte(content)
	}
	content := string(templateBytes)
	targetPath := filepath.Join(cwd, "AGENTS.md")
	if strings.Contains(codexHomeDir, filepath.Join(cwd, ".nana", "codex-home-investigate")) {
		targetPath = filepath.Join(codexHomeDir, "AGENTS.md")
		content = strings.ReplaceAll(content, "~/.codex", "./.nana/codex-home-investigate")
		content = addGeneratedAgentsMarker(content)
		return writeFileIfChanged(targetPath, content, options)
	}
	if strings.Contains(codexHomeDir, filepath.Join(cwd, ".codex")) {
		content = strings.ReplaceAll(content, "~/.codex", "./.codex")
		if fileExists(targetPath) && !options.Force {
			fmt.Fprintln(os.Stdout, "Skipped AGENTS.md overwrite")
			return nil
		}
	} else {
		targetPath = filepath.Join(codexHomeDir, "AGENTS.md")
		content = addGeneratedAgentsMarker(content)
	}
	return writeFileIfChanged(targetPath, content, options)
}

func addGeneratedAgentsMarker(content string) string {
	if strings.Contains(content, "<!-- nana:generated:agents-md -->") {
		return content
	}
	marker := "<!-- END AUTONOMY DIRECTIVE -->"
	index := strings.Index(content, marker)
	if index >= 0 {
		insertAt := index + len(marker)
		if insertAt < len(content) && content[insertAt] == '\n' {
			insertAt++
		}
		return content[:insertAt] + "<!-- nana:generated:agents-md -->\n" + content[insertAt:]
	}
	return "<!-- nana:generated:agents-md -->\n" + content
}

func copyFileIfChanged(src string, dst string, options SetupOptions) error {
	content, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return writeBytesIfChanged(dst, content, options)
}

func writeFileIfChanged(path string, content string, options SetupOptions) error {
	return writeBytesIfChanged(path, []byte(content), options)
}

func writeBytesIfChanged(path string, content []byte, options SetupOptions) error {
	if existing, err := os.ReadFile(path); err == nil {
		if string(existing) == string(content) {
			return nil
		}
	}
	if options.DryRun {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, content, 0o644)
}

func copyDirIfChanged(srcDir string, dstDir string, options SetupOptions) error {
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return err
	}
	if !options.DryRun {
		if err := os.MkdirAll(dstDir, 0o755); err != nil {
			return err
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		srcPath := filepath.Join(srcDir, entry.Name())
		dstPath := filepath.Join(dstDir, entry.Name())
		if entry.IsDir() {
			if err := copyDirIfChanged(srcPath, dstPath, options); err != nil {
				return err
			}
		} else {
			if err := copyFileIfChanged(srcPath, dstPath, options); err != nil {
				return err
			}
		}
	}
	return nil
}
