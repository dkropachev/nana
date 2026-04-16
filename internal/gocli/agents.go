package gocli

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const AgentsUsage = "Usage:\n  nana agents list [--scope user|project]\n  nana agents add <name> [--scope user|project] [--force]\n  nana agents edit <name> [--scope user|project]\n  nana agents remove <name> [--scope user|project] [--force]\n\nManage Codex native agent TOML files under ~/.codex/agents/ or ./.codex/agents/.\n\nNotes:\n  - list shows project + user agents by default\n  - add defaults to project scope when this repo is set up for project scope; otherwise user\n  - remove prompts for confirmation unless --force is passed"

var reservedNativeAgentNames = map[string]bool{
	"default":  true,
	"worker":   true,
	"explorer": true,
}

const defaultAgentModel = "gpt-5.4"

type nativeAgentInfo struct {
	Scope       string
	Path        string
	Name        string
	Description string
	Model       string
}

func Agents(cwd string, args []string) error {
	if len(args) == 0 || containsAny(args, "--help", "-h") {
		fmt.Fprintln(os.Stdout, AgentsUsage)
		return nil
	}

	subcommand := args[0]
	scope, err := parseAgentScopeArg(args[1:])
	if err != nil {
		return err
	}
	force := containsAny(args[1:], "--force")

	switch subcommand {
	case "list":
		agents, err := listNativeAgents(cwd, scope)
		if err != nil {
			return err
		}
		printAgentsTable(agents)
		return nil
	case "add":
		if len(args) < 2 || strings.HasPrefix(args[1], "-") {
			return fmt.Errorf("Usage: nana agents add <name>")
		}
		path, err := addNativeAgent(cwd, args[1], scope, force)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "Created native agent: %s\n", path)
		return nil
	case "edit":
		if len(args) < 2 || strings.HasPrefix(args[1], "-") {
			return fmt.Errorf("Usage: nana agents edit <name>")
		}
		path, err := editNativeAgent(cwd, args[1], scope)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "Edited native agent: %s\n", path)
		return nil
	case "remove":
		if len(args) < 2 || strings.HasPrefix(args[1], "-") {
			return fmt.Errorf("Usage: nana agents remove <name>")
		}
		path, err := removeNativeAgent(cwd, args[1], scope, force)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "Removed native agent: %s\n", path)
		return nil
	default:
		return fmt.Errorf("unknown agents subcommand: %s", subcommand)
	}
}

func parseAgentScopeArg(args []string) (string, error) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--scope":
			if i+1 >= len(args) {
				return "", fmt.Errorf("Expected --scope user|project")
			}
			if args[i+1] == "user" || args[i+1] == "project" {
				return args[i+1], nil
			}
			return "", fmt.Errorf("Expected --scope user|project")
		case "--scope=user":
			return "user", nil
		case "--scope=project":
			return "project", nil
		}
	}
	return "", nil
}

func normalizeAgentName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", fmt.Errorf("agent name must not be empty")
	}
	for _, ch := range trimmed {
		if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '_' || ch == '-') {
			return "", fmt.Errorf("invalid agent name: %s", name)
		}
	}
	if reservedNativeAgentNames[trimmed] {
		return "", fmt.Errorf("%q is reserved by Codex built-in agents", trimmed)
	}
	return trimmed, nil
}

func resolveAgentsDir(cwd string, scope string) string {
	if scope == "project" {
		return filepath.Join(cwd, ".codex", "agents")
	}
	return filepath.Join(CodexHome(), "agents")
}

func inferMutationScope(cwd string) string {
	scopePath := filepath.Join(cwd, ".nana", "setup-scope.json")
	if content, err := os.ReadFile(scopePath); err == nil {
		text := string(content)
		if strings.Contains(text, `"scope":"project"`) || strings.Contains(text, `"scope": "project"`) ||
			strings.Contains(text, `"scope":"project-local"`) || strings.Contains(text, `"scope": "project-local"`) {
			return "project"
		}
		if strings.Contains(text, `"scope":"user"`) || strings.Contains(text, `"scope": "user"`) {
			return "user"
		}
	}
	if fileExists(filepath.Join(cwd, ".codex")) {
		return "project"
	}
	return "user"
}

func agentFilePath(cwd string, name string, scope string) string {
	return filepath.Join(resolveAgentsDir(cwd, scope), name+".toml")
}

func scaffoldAgentToml(name string) string {
	return strings.Join([]string{
		fmt.Sprintf("# Codex native agent: %s", name),
		fmt.Sprintf(`name = "%s"`, name),
		`description = "TODO: describe this agent's purpose"`,
		`developer_instructions = """`,
		`TODO: add the operating instructions for this agent.`,
		`"""`,
		``,
		`# Optional fields:`,
		fmt.Sprintf(`# model = "%s"`, defaultAgentModel),
		`# model_reasoning_effort = "medium"`,
		`# temperature = 0.2`,
		`# tools = ["shell", "apply_patch"]`,
		``,
	}, "\n")
}

func parseAgentInfo(path string, scope string) nativeAgentInfo {
	content, err := os.ReadFile(path)
	fallbackName := strings.TrimSuffix(filepath.Base(path), ".toml")
	if err != nil {
		return nativeAgentInfo{Scope: scope, Path: path, Name: fallbackName, Description: "<invalid TOML>"}
	}
	text := string(content)
	name := readTomlQuotedValue(text, "name")
	if name == "" {
		name = fallbackName
	}
	description := readTomlQuotedValue(text, "description")
	if description == "" {
		description = "-"
	}
	model := readTomlQuotedValue(text, "model")
	if model == "" {
		model = "-"
	}
	return nativeAgentInfo{
		Scope:       scope,
		Path:        path,
		Name:        name,
		Description: description,
		Model:       model,
	}
}

func readTomlQuotedValue(content string, key string) string {
	for _, line := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, key+" = ") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(line, key+" = "))
		if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
			return value[1 : len(value)-1]
		}
	}
	return ""
}

func listNativeAgents(cwd string, scope string) ([]nativeAgentInfo, error) {
	if scope != "" {
		return readScopeAgents(cwd, scope)
	}
	projectAgents, err := readScopeAgents(cwd, "project")
	if err != nil {
		return nil, err
	}
	userAgents, err := readScopeAgents(cwd, "user")
	if err != nil {
		return nil, err
	}
	combined := append(projectAgents, userAgents...)
	sort.Slice(combined, func(i, j int) bool {
		if combined[i].Name == combined[j].Name {
			return combined[i].Scope < combined[j].Scope
		}
		return combined[i].Name < combined[j].Name
	})
	return combined, nil
}

func readScopeAgents(cwd string, scope string) ([]nativeAgentInfo, error) {
	dir := resolveAgentsDir(cwd, scope)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	agents := []nativeAgentInfo{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".toml") {
			continue
		}
		agents = append(agents, parseAgentInfo(filepath.Join(dir, entry.Name()), scope))
	}
	sort.Slice(agents, func(i, j int) bool {
		if agents[i].Name == agents[j].Name {
			return agents[i].Scope < agents[j].Scope
		}
		return agents[i].Name < agents[j].Name
	})
	return agents, nil
}

func addNativeAgent(cwd string, name string, scope string, force bool) (string, error) {
	normalized, err := normalizeAgentName(name)
	if err != nil {
		return "", err
	}
	if scope == "" {
		scope = inferMutationScope(cwd)
	}
	path := agentFilePath(cwd, normalized, scope)
	if fileExists(path) && !force {
		return "", fmt.Errorf("agent already exists: %s", path)
	}
	if err := os.MkdirAll(resolveAgentsDir(cwd, scope), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(scaffoldAgentToml(normalized)), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func resolveExistingAgentPath(cwd string, name string, scope string) (string, error) {
	normalized, err := normalizeAgentName(name)
	if err != nil {
		return "", err
	}
	candidateScopes := []string{"project", "user"}
	if scope != "" {
		candidateScopes = []string{scope}
	}
	for _, candidateScope := range candidateScopes {
		path := agentFilePath(cwd, normalized, candidateScope)
		if fileExists(path) {
			return path, nil
		}
	}
	return "", fmt.Errorf("agent not found: %s", normalized)
}

func editNativeAgent(cwd string, name string, scope string) (string, error) {
	path, err := resolveExistingAgentPath(cwd, name, scope)
	if err != nil {
		return "", err
	}
	editor := strings.TrimSpace(os.Getenv("EDITOR"))
	if editor == "" {
		editor = strings.TrimSpace(os.Getenv("VISUAL"))
	}
	if editor == "" {
		editor = "vi"
	}
	cmd := exec.Command(editor, path)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("editor exited with status %d", exitErr.ExitCode())
		}
		return "", err
	}
	return path, nil
}

func removeNativeAgent(cwd string, name string, scope string, force bool) (string, error) {
	path, err := resolveExistingAgentPath(cwd, name, scope)
	if err != nil {
		return "", err
	}
	if !force {
		if !isInteractiveTerminal() {
			return "", fmt.Errorf("remove requires an interactive terminal; rerun with --force in non-interactive environments")
		}
		confirmed, err := confirmRemove(path)
		if err != nil {
			return "", err
		}
		if !confirmed {
			return "", fmt.Errorf("remove aborted by user (pass --force to skip confirmation)")
		}
	}
	if err := os.Remove(path); err != nil {
		return "", err
	}
	return path, nil
}

func isInteractiveTerminal() bool {
	info, err := os.Stdin.Stat()
	if err != nil || (info.Mode()&os.ModeCharDevice) == 0 {
		return false
	}
	info, err = os.Stdout.Stat()
	if err != nil || (info.Mode()&os.ModeCharDevice) == 0 {
		return false
	}
	return true
}

func confirmRemove(path string) (bool, error) {
	fmt.Fprintf(os.Stdout, "Delete native agent %s? [y/N]: ", path)
	reader := bufio.NewReader(os.Stdin)
	answer, err := reader.ReadString('\n')
	if err != nil && len(answer) == 0 {
		return false, err
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes", nil
}

func printAgentsTable(agents []nativeAgentInfo) {
	if len(agents) == 0 {
		fmt.Fprintln(os.Stdout, "No native agents found.")
		return
	}
	rows := [][]string{{"scope", "name", "model", "description"}}
	for _, agent := range agents {
		rows = append(rows, []string{agent.Scope, agent.Name, agent.Model, agent.Description})
	}
	widths := make([]int, len(rows[0]))
	for _, row := range rows {
		for i, cell := range row {
			if len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}
	for _, row := range rows {
		parts := make([]string, len(row))
		for i, cell := range row {
			parts[i] = cell + strings.Repeat(" ", widths[i]-len(cell))
		}
		fmt.Fprintln(os.Stdout, strings.Join(parts, "  "))
	}
}
