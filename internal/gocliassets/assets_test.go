package gocliassets

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPromptAssetsStayInSyncWithPromptFiles(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	prompts, err := Prompts()
	if err != nil {
		t.Fatalf("Prompts(): %v", err)
	}
	for _, name := range []string{"executor.md", "team-executor.md", "investigator.md", "investigation-validator.md", "improvement-scout.md", "enhancement-scout.md", "ui-scout.md", "product-analyst.md"} {
		diskContent, err := os.ReadFile(filepath.Join(repoRoot, "prompts", name))
		if err != nil {
			t.Fatalf("read prompt %s: %v", name, err)
		}
		embedded, ok := prompts[name]
		if !ok {
			t.Fatalf("embedded prompts missing %s", name)
		}
		if embedded != string(diskContent) {
			t.Fatalf("embedded prompt %s is out of sync with prompts/%s", name, name)
		}
	}
}

func TestPrimaryPromptAssetsStayWithinBudget(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	budgets := map[string]int{
		"executor.md":          4096,
		"critic.md":            4096,
		"explore.md":           4096,
		"test-engineer.md":     4096,
		"security-reviewer.md": 4096,
		"quality-reviewer.md":  4096,
		"architect.md":         3072,
	}
	for name, budget := range budgets {
		content, err := os.ReadFile(filepath.Join(repoRoot, "prompts", name))
		if err != nil {
			t.Fatalf("read prompt %s: %v", name, err)
		}
		if len(content) > budget {
			t.Fatalf("prompt %s exceeds budget: %d > %d", name, len(content), budget)
		}
	}
}

func TestCompactEmbeddedPromptsStayInSyncAndWithinBudget(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	prompts, err := Prompts()
	if err != nil {
		t.Fatalf("Prompts(): %v", err)
	}
	for _, name := range []string{
		"executor-embedded.md",
		"critic-embedded.md",
		"test-engineer-embedded.md",
		"quality-reviewer-embedded.md",
		"security-reviewer-embedded.md",
		"qa-tester-embedded.md",
	} {
		diskContent, err := os.ReadFile(filepath.Join(repoRoot, "prompts", name))
		if err != nil {
			t.Fatalf("read prompt %s: %v", name, err)
		}
		embedded, ok := prompts[name]
		if !ok {
			t.Fatalf("embedded prompts missing %s", name)
		}
		if embedded != string(diskContent) {
			t.Fatalf("embedded prompt %s is out of sync with prompts/%s", name, name)
		}
		if len(diskContent) > 3072 {
			t.Fatalf("compact prompt %s exceeds budget: %d", name, len(diskContent))
		}
	}
}

func TestSkillAssetsStayInSyncWithSkillFiles(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	skills, err := Skills()
	if err != nil {
		t.Fatalf("Skills(): %v", err)
	}
	for _, name := range []string{
		"ai-slop-cleaner/SKILL.md",
		"autopilot/SKILL.md",
		"deep-interview/SKILL.md",
		"pipeline/SKILL.md",
		"trace/SKILL.md",
		"visual-verdict/SKILL.md",
		"web-clone/SKILL.md",
	} {
		diskContent, err := os.ReadFile(filepath.Join(repoRoot, "skills", filepath.FromSlash(name)))
		if err != nil {
			t.Fatalf("read skill %s: %v", name, err)
		}
		embedded, ok := skills[name]
		if !ok {
			t.Fatalf("embedded skills missing %s", name)
		}
		if embedded != string(diskContent) {
			t.Fatalf("embedded skill %s is out of sync with skills/%s", name, name)
		}
	}
}

func TestRuntimeSkillAssetsStayInSyncAndWithinBudget(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	skills, err := Skills()
	if err != nil {
		t.Fatalf("Skills(): %v", err)
	}
	for _, name := range []string{
		"autopilot/RUNTIME.md",
		"ultrawork/RUNTIME.md",
		"analyze/RUNTIME.md",
		"plan/RUNTIME.md",
		"deep-interview/RUNTIME.md",
		"ralplan/RUNTIME.md",
		"ecomode/RUNTIME.md",
		"cancel/RUNTIME.md",
		"tdd/RUNTIME.md",
		"build-fix/RUNTIME.md",
		"code-review/RUNTIME.md",
		"security-review/RUNTIME.md",
		"web-clone/RUNTIME.md",
	} {
		diskContent, err := os.ReadFile(filepath.Join(repoRoot, "skills", filepath.FromSlash(name)))
		if err != nil {
			t.Fatalf("read runtime skill %s: %v", name, err)
		}
		embedded, ok := skills[name]
		if !ok {
			t.Fatalf("embedded runtime skill missing %s", name)
		}
		if embedded != string(diskContent) {
			t.Fatalf("embedded runtime skill %s is out of sync", name)
		}
		if len(diskContent) > 3072 {
			t.Fatalf("runtime skill %s exceeds budget: %d", name, len(diskContent))
		}
	}
}

func TestTemplateAssetsStayInSyncWithTemplateFiles(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	templates, err := Templates()
	if err != nil {
		t.Fatalf("Templates(): %v", err)
	}
	diskContent, err := os.ReadFile(filepath.Join(repoRoot, "templates", "AGENTS.md"))
	if err != nil {
		t.Fatalf("read template AGENTS.md: %v", err)
	}
	embedded, ok := templates["AGENTS.md"]
	if !ok {
		t.Fatalf("embedded templates missing AGENTS.md")
	}
	if embedded != string(diskContent) {
		t.Fatalf("embedded template AGENTS.md is out of sync with templates/AGENTS.md")
	}
	const compactAgentsBudget = 6 * 1024
	if len(diskContent) > compactAgentsBudget {
		t.Fatalf("template AGENTS.md exceeds compact budget: %d > %d", len(diskContent), compactAgentsBudget)
	}
	if !strings.Contains(string(diskContent), "## Lazy Runtime Skills") {
		t.Fatalf("template AGENTS.md should route rarely used modes through lazy runtime skills")
	}
	activationNeedle := "When a listed keyword matches, invoke that `$skill` by reading its RUNTIME.md."
	if !strings.Contains(string(diskContent), activationNeedle) {
		t.Fatalf("template AGENTS.md should explicitly activate runtime skills for keyword matches")
	}
	setupNeedle := "write generated `AGENTS.md` to the selected AGENTS target"
	if !strings.Contains(string(diskContent), setupNeedle) {
		t.Fatalf("template AGENTS.md should describe the AGENTS target without putting AGENTS.md under a Codex home")
	}
	for _, staleSetupGuidance := range []string{
		"`AGENTS.md` under `~/.codex`",
		"`AGENTS.md` under `./.codex`",
	} {
		if strings.Contains(string(diskContent), staleSetupGuidance) {
			t.Fatalf("template AGENTS.md should not point setup guidance at stale path %q", staleSetupGuidance)
		}
	}
	for _, verboseBlock := range []string{
		"<keyword_detection>",
		"<execution_protocols>",
		"<state_management>",
		"<delegation_rules>",
		"<child_agent_protocol>",
		"<model_routing>",
	} {
		if strings.Contains(string(diskContent), verboseBlock) {
			t.Fatalf("template AGENTS.md still contains verbose generated block %q", verboseBlock)
		}
	}
	rootAgents, err := os.ReadFile(filepath.Join(repoRoot, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read root AGENTS.md: %v", err)
	}
	if len(rootAgents) > compactAgentsBudget {
		t.Fatalf("root AGENTS.md exceeds compact budget: %d > %d", len(rootAgents), compactAgentsBudget)
	}
	if !strings.Contains(string(rootAgents), activationNeedle) {
		t.Fatalf("root AGENTS.md should explicitly activate runtime skills for keyword matches")
	}
	if !strings.Contains(string(rootAgents), setupNeedle) || !strings.Contains(string(rootAgents), "`./AGENTS.md` for project scope") {
		t.Fatalf("root AGENTS.md should point generated project guidance at ./AGENTS.md")
	}
	if strings.Contains(string(rootAgents), "`AGENTS.md` under `./.codex`") {
		t.Fatalf("root AGENTS.md should not point generated AGENTS.md at ./.codex")
	}
	for _, needle := range []string{
		"`~/.codex/skills/autopilot/RUNTIME.md`",
		"`~/.codex/skills/deep-interview/RUNTIME.md`",
		"`~/.codex/skills/security-review/RUNTIME.md`",
		"`~/.codex/skills/web-clone/RUNTIME.md`",
		"`nana route --explain \"<prompt>\"` to preview routing",
		"`routing_decision` in plans, traces, and final reports",
		"`role_tier` (tier/roles)",
	} {
		if !strings.Contains(string(diskContent), needle) {
			t.Fatalf("template AGENTS.md missing expected guidance %q", needle)
		}
		if !strings.Contains(string(rootAgents), strings.ReplaceAll(needle, "~/.codex", "./.codex")) {
			t.Fatalf("root AGENTS.md missing expected guidance %q", needle)
		}
	}
}

func TestGeneratedAgentsVerifyGuidanceRequiresRepoProfileOrDocumentedFallback(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	templates, err := Templates()
	if err != nil {
		t.Fatalf("Templates(): %v", err)
	}
	diskContent, err := os.ReadFile(filepath.Join(repoRoot, "templates", "AGENTS.md"))
	if err != nil {
		t.Fatalf("read template AGENTS.md: %v", err)
	}
	rootAgents, err := os.ReadFile(filepath.Join(repoRoot, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read root AGENTS.md: %v", err)
	}
	requiredNeedles := []string{
		"Prefer `nana verify --json` when `nana-verify.json` exists",
		"otherwise use documented repo verification commands",
	}
	forbiddenNeedles := []string{
		"its profile runs lint, typecheck, tests, and static analysis",
	}
	for _, source := range []struct {
		name    string
		content string
	}{
		{name: "templates/AGENTS.md", content: string(diskContent)},
		{name: "generated template AGENTS.md", content: templates["AGENTS.md"]},
		{name: "root AGENTS.md", content: string(rootAgents)},
	} {
		for _, needle := range requiredNeedles {
			if !strings.Contains(source.content, needle) {
				t.Fatalf("%s missing conditional verify guidance %q", source.name, needle)
			}
		}
		for _, needle := range forbiddenNeedles {
			if strings.Contains(source.content, needle) {
				t.Fatalf("%s should not unconditionally claim nana verify profile behavior %q", source.name, needle)
			}
		}
	}
}

func TestGeneratedAgentsSkillTriggerGuidancePreservesCaseInsensitiveContract(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	templates, err := Templates()
	if err != nil {
		t.Fatalf("Templates(): %v", err)
	}
	diskContent, err := os.ReadFile(filepath.Join(repoRoot, "templates", "AGENTS.md"))
	if err != nil {
		t.Fatalf("read template AGENTS.md: %v", err)
	}
	rootAgents, err := os.ReadFile(filepath.Join(repoRoot, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read root AGENTS.md: %v", err)
	}
	needle := "keyword matches are case-insensitive"
	for _, source := range []struct {
		name    string
		content string
	}{
		{name: "templates/AGENTS.md", content: string(diskContent)},
		{name: "generated template AGENTS.md", content: templates["AGENTS.md"]},
		{name: "root AGENTS.md", content: string(rootAgents)},
	} {
		if !strings.Contains(source.content, needle) {
			t.Fatalf("%s must preserve case-insensitive skill trigger guidance", source.name)
		}
	}
}

func TestGeneratedAgentsSkillTriggerGuidancePreservesExplicitPrecedenceContract(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	templates, err := Templates()
	if err != nil {
		t.Fatalf("Templates(): %v", err)
	}
	diskContent, err := os.ReadFile(filepath.Join(repoRoot, "templates", "AGENTS.md"))
	if err != nil {
		t.Fatalf("read template AGENTS.md: %v", err)
	}
	rootAgents, err := os.ReadFile(filepath.Join(repoRoot, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read root AGENTS.md: %v", err)
	}
	needle := "Explicit `$skill` invocations run left-to-right before implicit keyword matches"
	for _, source := range []struct {
		name    string
		content string
	}{
		{name: "templates/AGENTS.md", content: string(diskContent)},
		{name: "generated template AGENTS.md", content: templates["AGENTS.md"]},
		{name: "root AGENTS.md", content: string(rootAgents)},
	} {
		if !strings.Contains(source.content, needle) {
			t.Fatalf("%s must preserve explicit skill precedence guidance", source.name)
		}
	}
}

func TestRoutingDecisionGuidanceIsEmbeddedForReportSurfaces(t *testing.T) {
	prompts, err := Prompts()
	if err != nil {
		t.Fatalf("Prompts(): %v", err)
	}
	skills, err := Skills()
	if err != nil {
		t.Fatalf("Skills(): %v", err)
	}
	templates, err := Templates()
	if err != nil {
		t.Fatalf("Templates(): %v", err)
	}
	surfaces := map[string]string{
		"templates/AGENTS.md":          templates["AGENTS.md"],
		"prompts/executor.md":          prompts["executor.md"],
		"prompts/executor-embedded.md": prompts["executor-embedded.md"],
		"skills/plan/RUNTIME.md":       skills["plan/RUNTIME.md"],
		"skills/ralplan/RUNTIME.md":    skills["ralplan/RUNTIME.md"],
		"skills/trace/SKILL.md":        skills["trace/SKILL.md"],
	}
	for name, content := range surfaces {
		if !strings.Contains(content, "routing_decision") {
			t.Fatalf("%s missing routing_decision guidance", name)
		}
	}
}

func TestCompactAgentsAskGatePreservesIrreversibleAndSideEffectfulActions(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	templates, err := Templates()
	if err != nil {
		t.Fatalf("Templates(): %v", err)
	}
	embedded, ok := templates["AGENTS.md"]
	if !ok {
		t.Fatalf("embedded templates missing AGENTS.md")
	}
	diskContent, err := os.ReadFile(filepath.Join(repoRoot, "templates", "AGENTS.md"))
	if err != nil {
		t.Fatalf("read template AGENTS.md: %v", err)
	}
	rootAgents, err := os.ReadFile(filepath.Join(repoRoot, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read root AGENTS.md: %v", err)
	}

	for _, source := range []struct {
		name    string
		content string
	}{
		{name: "templates/AGENTS.md", content: string(diskContent)},
		{name: "generated template AGENTS.md", content: embedded},
		{name: "root AGENTS.md", content: string(rootAgents)},
	} {
		for _, needle := range []string{
			"Proceed automatically on clear, safe, low-risk, reversible tasks",
			"ask for ambiguous, destructive, irreversible, externally side-effectful, or materially branching choices",
		} {
			if !strings.Contains(source.content, needle) {
				t.Fatalf("%s ask gate must retain safety boundary %q", source.name, needle)
			}
		}
	}
}
