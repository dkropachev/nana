package gocli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const RouteUsage = `nana route - Preview NANA prompt-to-skill routing

Usage:
  nana route --explain <prompt>
  nana route explain <prompt>

Behavior:
  - reports explicit $skill invocations before implicit keyword matches
  - matches implicit keywords case-insensitively and anywhere on token boundaries
  - prints the matched trigger, precedence source, and runtime/skill doc path
  - honors /prompts:<name> suppression of implicit keyword routing
`

type routeRule struct {
	Skill    string
	Keywords []string
}

type routeActivation struct {
	Skill   string
	Source  string
	Trigger string
	// RuntimePath is the document path shown by the route preview. It points to
	// RUNTIME.md for lazy runtime skills and falls back to SKILL.md for regular
	// installed skills that do not ship a compact runtime document.
	RuntimePath string
	DocLabel    string
	Start       int
}

type routePreview struct {
	Prompt               string
	Activations          []routeActivation
	ImplicitSuppressedBy string
	NoActivationReason   string
}

type keywordRouteCandidate struct {
	Activation routeActivation
	RuleOrder  int
}

type routeDoc struct {
	Path  string
	Label string
}

type routeDocBase struct {
	actualSkillsDir    string
	displaySkillsDir   string
	displayDotRelative bool
}

const (
	routeDocLabelRuntime = "runtime"
	routeDocLabelSkill   = "skill"
)

// Keep routeRules synchronized with the Lazy Runtime Skills mapping in
// AGENTS.md and templates/AGENTS.md.
var routeRules = []routeRule{
	{Skill: "autopilot", Keywords: []string{"autopilot", "build me", "I want a"}},
	{Skill: "ultrawork", Keywords: []string{"ultrawork", "ulw", "parallel"}},
	{Skill: "analyze", Keywords: []string{"analyze", "investigate"}},
	{Skill: "plan", Keywords: []string{"plan this", "plan the", "let's plan"}},
	{Skill: "deep-interview", Keywords: []string{"interview", "deep interview", "gather requirements", "interview me", "don't assume", "ouroboros"}},
	{Skill: "ralplan", Keywords: []string{"ralplan", "consensus plan"}},
	{Skill: "ecomode", Keywords: []string{"ecomode", "eco", "budget"}},
	{Skill: "cancel", Keywords: []string{"cancel", "stop", "abort"}},
	{Skill: "tdd", Keywords: []string{"tdd", "test first"}},
	{Skill: "build-fix", Keywords: []string{"fix build", "type errors"}},
	{Skill: "code-review", Keywords: []string{"review code", "code review", "code-review"}},
	{Skill: "security-review", Keywords: []string{"security review"}},
	{Skill: "web-clone", Keywords: []string{"web-clone", "clone site", "clone website", "copy webpage"}},
}

var (
	explicitRouteSkillPattern = regexp.MustCompile(`(^|[^A-Za-z0-9_])\$([A-Za-z][A-Za-z0-9_-]*)`)
	promptInvocationPattern   = regexp.MustCompile(`(^|\s)(/prompts:[^\s]+)`)
)

func Route(cwd string, args []string) error {
	if len(args) == 0 || isHelpToken(args[0]) {
		fmt.Fprint(os.Stdout, RouteUsage)
		return nil
	}

	prompt, err := parseRouteExplainPrompt(args)
	if err != nil {
		return err
	}
	preview := ExplainPromptRouteForCWD(cwd, prompt)
	fmt.Fprint(os.Stdout, FormatRoutePreview(preview))
	return nil
}

func parseRouteExplainPrompt(args []string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("missing prompt\n\n%s", RouteUsage)
	}

	var promptArgs []string
	switch args[0] {
	case "--explain", "explain":
		promptArgs = args[1:]
	default:
		return "", fmt.Errorf("unknown route option: %s\n\n%s", args[0], RouteUsage)
	}
	if len(promptArgs) > 0 && promptArgs[0] == "--" {
		promptArgs = promptArgs[1:]
	}
	prompt := strings.TrimSpace(strings.Join(promptArgs, " "))
	if prompt == "" {
		return "", fmt.Errorf("missing prompt\n\n%s", RouteUsage)
	}
	return prompt, nil
}

func ExplainPromptRoute(prompt string) routePreview {
	return explainPromptRoute(prompt, routeRuntimeDocResolver(), isKnownRouteSkill)
}

func ExplainPromptRouteForCWD(cwd string, prompt string) routePreview {
	return explainPromptRoute(prompt, routeDocResolver(cwd), routeExplicitSkillValidator(cwd))
}

func explainPromptRoute(prompt string, docPath func(string) routeDoc, validExplicitSkill func(string) bool) routePreview {
	preview := routePreview{Prompt: prompt}
	seenSkills := map[string]bool{}

	for _, match := range explicitRouteSkillPattern.FindAllStringSubmatchIndex(prompt, -1) {
		fullStart, fullEnd := match[0], match[1]
		nameStart, nameEnd := match[4], match[5]
		full := prompt[fullStart:fullEnd]
		dollarOffset := strings.Index(full, "$")
		if dollarOffset < 0 {
			continue
		}
		start := fullStart + dollarOffset
		skill := strings.ToLower(prompt[nameStart:nameEnd])
		if !validExplicitSkill(skill) {
			continue
		}
		if seenSkills[skill] {
			continue
		}
		seenSkills[skill] = true
		doc := docPath(skill)
		preview.Activations = append(preview.Activations, routeActivation{
			Skill:       skill,
			Source:      "explicit invocation",
			Trigger:     prompt[start:fullEnd],
			RuntimePath: doc.Path,
			DocLabel:    doc.Label,
			Start:       start,
		})
	}

	if promptInvocation := firstPromptInvocation(prompt); promptInvocation != "" {
		preview.ImplicitSuppressedBy = promptInvocation
		if len(preview.Activations) == 0 {
			preview.NoActivationReason = fmt.Sprintf("%s suppresses implicit keyword routing; add an explicit $skill token to activate a skill.", promptInvocation)
		}
		return preview
	}

	keywordCandidates := []keywordRouteCandidate{}
	for ruleOrder, rule := range routeRules {
		if seenSkills[rule.Skill] {
			continue
		}
		match, ok := bestKeywordMatch(prompt, rule.Keywords)
		if !ok {
			continue
		}
		doc := docPath(rule.Skill)
		keywordCandidates = append(keywordCandidates, keywordRouteCandidate{
			Activation: routeActivation{
				Skill:       rule.Skill,
				Source:      "implicit keyword",
				Trigger:     match.trigger,
				RuntimePath: doc.Path,
				DocLabel:    doc.Label,
				Start:       match.start,
			},
			RuleOrder: ruleOrder,
		})
	}
	sort.SliceStable(keywordCandidates, func(i, j int) bool {
		left := keywordCandidates[i]
		right := keywordCandidates[j]
		if left.Activation.Start != right.Activation.Start {
			return left.Activation.Start < right.Activation.Start
		}
		return left.RuleOrder < right.RuleOrder
	})
	for _, candidate := range keywordCandidates {
		preview.Activations = append(preview.Activations, candidate.Activation)
	}

	if len(preview.Activations) == 0 {
		preview.NoActivationReason = "No explicit $skill invocation or mapped keyword matched."
	}
	return preview
}

func isKnownRouteSkill(skill string) bool {
	for _, rule := range routeRules {
		if rule.Skill == skill {
			return true
		}
	}
	return false
}

func routeExplicitSkillValidator(cwd string) func(string) bool {
	base := routeDocBaseForCWD(cwd)
	return func(skill string) bool {
		if isKnownRouteSkill(skill) {
			return true
		}
		_, ok := installedRouteSkillDoc(base, skill)
		return ok
	}
}

func installedRouteSkillDoc(base routeDocBase, skill string) (routeDoc, bool) {
	skill = strings.TrimSpace(skill)
	if skill == "" {
		return routeDoc{}, false
	}
	candidates := []struct {
		filename string
		label    string
	}{
		{filename: "RUNTIME.md", label: routeDocLabelRuntime},
		{filename: "SKILL.md", label: routeDocLabelSkill},
	}
	for _, candidate := range candidates {
		actualPath := filepath.Join(base.actualSkillsDir, skill, candidate.filename)
		info, err := os.Stat(actualPath)
		if err != nil || info.IsDir() {
			continue
		}
		return routeDoc{
			Path:  base.displayDocPath(skill, candidate.filename),
			Label: candidate.label,
		}, true
	}
	return routeDoc{}, false
}

type routeKeywordMatch struct {
	start   int
	end     int
	trigger string
}

func bestKeywordMatch(prompt string, keywords []string) (routeKeywordMatch, bool) {
	best := routeKeywordMatch{start: len(prompt) + 1}
	for _, keyword := range keywords {
		pattern, err := keywordBoundaryPattern(keyword)
		if err != nil {
			continue
		}
		indexes := pattern.FindStringSubmatchIndex(prompt)
		if len(indexes) < 6 || indexes[4] < 0 || indexes[5] < 0 {
			continue
		}
		start, end := indexes[4], indexes[5]
		if start < best.start || (start == best.start && end-start > best.end-best.start) {
			best = routeKeywordMatch{start: start, end: end, trigger: prompt[start:end]}
		}
	}
	if best.start > len(prompt) {
		return routeKeywordMatch{}, false
	}
	return best, true
}

func keywordBoundaryPattern(keyword string) (*regexp.Regexp, error) {
	// Capture only the keyword while allowing punctuation/whitespace delimiters
	// around it. Unicode letters/numbers/underscore are treated as token chars.
	return regexp.Compile(`(?i)(^|[^\pL\pN_])(` + regexp.QuoteMeta(keyword) + `)([^\pL\pN_]|$)`)
}

func firstPromptInvocation(prompt string) string {
	match := promptInvocationPattern.FindStringSubmatchIndex(prompt)
	if len(match) == 0 {
		return ""
	}
	return prompt[match[4]:match[5]]
}

func routeRuntimePath(skill string) string {
	return filepath.Join(CodexHome(), "skills", skill, "RUNTIME.md")
}

func routeRuntimeDocResolver() func(string) routeDoc {
	return func(skill string) routeDoc {
		return routeDoc{
			Path:  routeRuntimePath(skill),
			Label: routeDocLabelRuntime,
		}
	}
}

func routeDocResolver(cwd string) func(string) routeDoc {
	base := routeDocBaseForCWD(cwd)
	return func(skill string) routeDoc {
		if !isKnownRouteSkill(skill) {
			if doc, ok := installedRouteSkillDoc(base, skill); ok {
				return doc
			}
		}
		return routeDoc{
			Path:  base.displayDocPath(skill, "RUNTIME.md"),
			Label: routeDocLabelRuntime,
		}
	}
}

func routeDocBaseForCWD(cwd string) routeDocBase {
	if codexHome := strings.TrimSpace(os.Getenv("CODEX_HOME")); codexHome != "" {
		return routeDocBase{
			actualSkillsDir:  filepath.Join(codexHome, "skills"),
			displaySkillsDir: filepath.Join(codexHome, "skills"),
		}
	}

	actualCodexHome := ResolveCodexHomeForLaunch(cwd)
	displayCodexHome := actualCodexHome
	displayDotRelative := false
	scope := "user"
	if cwd != "" {
		scope, _ = resolveDoctorScope(cwd)
	}
	if scope == "project" {
		displayCodexHome = ".codex"
		displayDotRelative = true
	}
	return routeDocBase{
		actualSkillsDir:    filepath.Join(actualCodexHome, "skills"),
		displaySkillsDir:   filepath.Join(displayCodexHome, "skills"),
		displayDotRelative: displayDotRelative,
	}
}

func (base routeDocBase) displayDocPath(skill string, filename string) string {
	displayPath := filepath.Join(base.displaySkillsDir, skill, filename)
	if base.displayDotRelative {
		return "." + string(os.PathSeparator) + displayPath
	}
	return displayPath
}

func FormatRoutePreview(preview routePreview) string {
	var builder strings.Builder
	fmt.Fprintln(&builder, "Route preview:")
	fmt.Fprintf(&builder, "Prompt: %s\n", preview.Prompt)
	if len(preview.Activations) == 0 {
		fmt.Fprintln(&builder, "Activations: none")
		if preview.NoActivationReason != "" {
			fmt.Fprintf(&builder, "Why: %s\n", preview.NoActivationReason)
		}
	} else {
		fmt.Fprintln(&builder, "Activations:")
		for index, activation := range preview.Activations {
			fmt.Fprintf(&builder, "  %d. $%s\n", index+1, activation.Skill)
			fmt.Fprintf(&builder, "     source: %s %q\n", activation.Source, activation.Trigger)
			fmt.Fprintf(&builder, "     why: %s\n", routeActivationWhy(activation))
			docLabel := activation.DocLabel
			if docLabel == "" {
				docLabel = routeDocLabelRuntime
			}
			fmt.Fprintf(&builder, "     %s: %s\n", docLabel, activation.RuntimePath)
			if activation.Skill == "ralplan" {
				fmt.Fprintln(&builder, "     note: ralplan remains planning-only until .nana/plans/prd-*.md and test-spec-*.md both exist")
			}
		}
	}
	if preview.ImplicitSuppressedBy != "" {
		fmt.Fprintf(&builder, "Implicit keywords: suppressed by %s\n", preview.ImplicitSuppressedBy)
	}
	fmt.Fprintln(&builder, "Rules: explicit $name invocations run first; implicit keywords are case-insensitive and match anywhere on token boundaries.")
	return builder.String()
}

func routeActivationWhy(activation routeActivation) string {
	switch activation.Source {
	case "explicit invocation":
		return "explicit $name invocations run left-to-right before implicit keyword routing"
	case "implicit keyword":
		return "case-insensitive keyword match anywhere in the prompt on token boundaries"
	default:
		return activation.Source
	}
}
