package gocli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const RouteUsage = `nana route - Preview NANA prompt-to-skill routing

Usage:
  nana route --explain <prompt>
  nana route explain <prompt>

Behavior:
  - reports explicit $skill invocations before implicit keyword matches
  - matches implicit keywords case-insensitively on token boundaries
  - requires an explicit user cancellation request before routing to $cancel; internal stop/completion guidance does not count
  - prints matched and ignored triggers with precedence/suppression reasons
  - prints the matched trigger, precedence source, and runtime/skill doc path
  - honors /prompts:<name> suppression of implicit keyword routing
`

type routeRule struct {
	Skill     string
	Keywords  []string
	MatchMode routeMatchMode
}

type routeActivation struct {
	Skill   string
	Source  string
	Trigger string
	// RuntimePath is the document path shown by the route preview. It points to
	// RUNTIME.md for lazy runtime skills and falls back to SKILL.md for regular
	// installed skills that do not ship a compact runtime document.
	RuntimePath       string
	RuntimeActualPath string
	DocLabel          string
	Start             int
}

type routeIgnoredTrigger struct {
	Skill   string
	Source  string
	Trigger string
	Reason  string
	Start   int
}

type routePreview struct {
	Prompt               string
	Activations          []routeActivation
	IgnoredTriggers      []routeIgnoredTrigger
	ImplicitSuppressedBy string
	NoActivationReason   string
}

type keywordRouteCandidate struct {
	Activation routeActivation
	RuleOrder  int
}

type routeDoc struct {
	Path       string
	ActualPath string
	Label      string
}

type routeDocBase struct {
	actualSkillsDir    string
	displaySkillsDir   string
	displayDotRelative bool
}

type routeMatchMode string

const (
	routeDocLabelRuntime = "runtime"
	routeDocLabelSkill   = "skill"

	routeSourceExplicitInvocation = "explicit invocation"
	routeSourceImplicitKeyword    = "implicit keyword"

	routeMatchModeTokenBoundary  routeMatchMode = ""
	routeMatchModeExplicitCancel routeMatchMode = "explicit-cancel"
)

// routeRules are loaded from the lazy skill trigger manifest, which also
// renders the Lazy Runtime Skills block in AGENTS.md/templates.
var routeRules = lazySkillTriggerRouteRules()

var (
	explicitRouteSkillPattern = regexp.MustCompile(`(^|[^A-Za-z0-9_])\$([A-Za-z][A-Za-z0-9_-]*)`)
	promptInvocationPattern   = regexp.MustCompile(`(^|\s)(/prompts:[A-Za-z0-9_.-]+)`)
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

func ExplainPromptRouteForCWDAndCodexHome(cwd string, codexHome string, prompt string) routePreview {
	codexHome = strings.TrimSpace(codexHome)
	if codexHome == "" {
		return ExplainPromptRouteForCWD(cwd, prompt)
	}
	base := routeDocBase{
		actualSkillsDir:  filepath.Join(codexHome, "skills"),
		displaySkillsDir: filepath.Join(codexHome, "skills"),
	}
	return explainPromptRoute(prompt, routeDocResolverForBase(base), routeExplicitSkillValidatorForBase(base))
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
			preview.IgnoredTriggers = append(preview.IgnoredTriggers, routeIgnoredTrigger{
				Source:  routeSourceExplicitInvocation,
				Trigger: prompt[start:fullEnd],
				Reason:  fmt.Sprintf("unknown skill %q; not listed in lazy runtime skills and no installed skill doc was found", skill),
				Start:   start,
			})
			continue
		}
		if seenSkills[skill] {
			preview.IgnoredTriggers = append(preview.IgnoredTriggers, routeIgnoredTrigger{
				Skill:   skill,
				Source:  routeSourceExplicitInvocation,
				Trigger: prompt[start:fullEnd],
				Reason:  fmt.Sprintf("duplicate explicit invocation; first $%s activation already wins", skill),
				Start:   start,
			})
			continue
		}
		seenSkills[skill] = true
		doc := docPath(skill)
		preview.Activations = append(preview.Activations, routeActivation{
			Skill:             skill,
			Source:            routeSourceExplicitInvocation,
			Trigger:           prompt[start:fullEnd],
			RuntimePath:       doc.Path,
			RuntimeActualPath: doc.ActualPath,
			DocLabel:          doc.Label,
			Start:             start,
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
		matches := keywordMatchesForRule(prompt, rule)
		if len(matches) == 0 {
			continue
		}
		if seenSkills[rule.Skill] {
			for _, match := range matches {
				preview.IgnoredTriggers = append(preview.IgnoredTriggers, routeIgnoredTrigger{
					Skill:   rule.Skill,
					Source:  routeSourceImplicitKeyword,
					Trigger: match.trigger,
					Reason:  fmt.Sprintf("ignored because explicit $%s activation takes precedence for the same skill", rule.Skill),
					Start:   match.start,
				})
			}
			continue
		}
		match := matches[0]
		doc := docPath(rule.Skill)
		keywordCandidates = append(keywordCandidates, keywordRouteCandidate{
			Activation: routeActivation{
				Skill:             rule.Skill,
				Source:            routeSourceImplicitKeyword,
				Trigger:           match.trigger,
				RuntimePath:       doc.Path,
				RuntimeActualPath: doc.ActualPath,
				DocLabel:          doc.Label,
				Start:             match.start,
			},
			RuleOrder: ruleOrder,
		})
		for _, ignored := range matches[1:] {
			preview.IgnoredTriggers = append(preview.IgnoredTriggers, routeIgnoredTrigger{
				Skill:   rule.Skill,
				Source:  routeSourceImplicitKeyword,
				Trigger: ignored.trigger,
				Reason:  fmt.Sprintf("ignored because implicit keyword %q already activates $%s; one activation per skill", match.trigger, rule.Skill),
				Start:   ignored.start,
			})
		}
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
	sortIgnoredTriggers(preview.IgnoredTriggers)

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
	return routeExplicitSkillValidatorForBase(routeDocBaseForCWD(cwd))
}

func routeExplicitSkillValidatorForBase(base routeDocBase) func(string) bool {
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
			Path:       base.displayDocPath(skill, candidate.filename),
			ActualPath: actualPath,
			Label:      candidate.label,
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
	matches := keywordMatches(prompt, keywords)
	if len(matches) == 0 {
		return routeKeywordMatch{}, false
	}
	return matches[0], true
}

func parseRouteMatchMode(raw string) (routeMatchMode, error) {
	mode := routeMatchMode(strings.TrimSpace(raw))
	switch mode {
	case routeMatchModeTokenBoundary, routeMatchModeExplicitCancel:
		return mode, nil
	default:
		return "", fmt.Errorf("unsupported route match_mode %q", raw)
	}
}

func keywordMatchesForRule(prompt string, rule routeRule) []routeKeywordMatch {
	switch rule.MatchMode {
	case routeMatchModeExplicitCancel:
		return explicitCancelMatches(prompt, rule.Keywords)
	case routeMatchModeTokenBoundary:
		return keywordMatches(prompt, rule.Keywords)
	default:
		return nil
	}
}

func keywordMatches(prompt string, keywords []string) []routeKeywordMatch {
	matches := []routeKeywordMatch{}
	for _, keyword := range keywords {
		pattern, err := keywordPattern(keyword)
		if err != nil {
			continue
		}
		for _, indexes := range pattern.FindAllStringIndex(prompt, -1) {
			if len(indexes) < 2 {
				continue
			}
			start, end := indexes[0], indexes[1]
			if !hasKeywordBoundaries(prompt, start, end) {
				continue
			}
			if start > 0 && prompt[start-1] == '$' {
				continue
			}
			matches = append(matches, routeKeywordMatch{start: start, end: end, trigger: prompt[start:end]})
		}
	}
	sort.SliceStable(matches, func(i, j int) bool {
		left := matches[i]
		right := matches[j]
		if left.start != right.start {
			return left.start < right.start
		}
		return left.end-left.start > right.end-right.start
	})
	return matches
}

type routeWord struct {
	start int
	end   int
	lower string
}

var (
	explicitCancelCourtesyWords = map[string]bool{
		"just":   true,
		"kindly": true,
		"please": true,
		"pls":    true,
		"really": true,
	}
	explicitCancelAcknowledgementWords = map[string]bool{
		"alright": true,
		"fine":    true,
		"hey":     true,
		"k":       true,
		"kk":      true,
		"ok":      true,
		"okay":    true,
		"sure":    true,
		"then":    true,
		"well":    true,
	}
	explicitCancelLeadInPhrases = [][]string{
		{"can", "you"},
		{"could", "you"},
		{"will", "you"},
		{"would", "you"},
		{"can", "we"},
		{"could", "we"},
		{"i", "want", "to"},
		{"i", "need", "to"},
		{"i", "d", "like", "to"},
		{"i", "would", "like", "to"},
		{"i", "d", "like", "you", "to"},
		{"i", "would", "like", "you", "to"},
		{"let", "s"},
		{"lets"},
	}
	explicitCancelConditionalWords = map[string]bool{
		"after":  true,
		"if":     true,
		"once":   true,
		"unless": true,
		"until":  true,
		"when":   true,
	}
	explicitCancelSuffixSkippableWords = map[string]bool{
		"and":         true,
		"away":        true,
		"cleanly":     true,
		"for":         true,
		"here":        true,
		"immediately": true,
		"just":        true,
		"kindly":      true,
		"now":         true,
		"please":      true,
		"pls":         true,
		"really":      true,
		"right":       true,
		"safely":      true,
		"there":       true,
		"up":          true,
	}
	explicitCancelTargetIntroWords = map[string]bool{
		"a":       true,
		"active":  true,
		"all":     true,
		"an":      true,
		"any":     true,
		"current": true,
		"my":      true,
		"our":     true,
		"that":    true,
		"the":     true,
		"these":   true,
		"this":    true,
		"those":   true,
		"your":    true,
	}
	explicitCancelTargetPronouns = map[string]bool{
		"everything": true,
		"it":         true,
		"me":         true,
		"them":       true,
		"us":         true,
	}
	explicitCancelBareObjectWords = map[string]bool{
		"execution": true,
		"flow":      true,
		"flows":     true,
		"job":       true,
		"jobs":      true,
		"mode":      true,
		"modes":     true,
		"process":   true,
		"processes": true,
		"run":       true,
		"runs":      true,
		"session":   true,
		"sessions":  true,
		"state":     true,
		"states":    true,
		"task":      true,
		"tasks":     true,
		"work":      true,
		"workflow":  true,
		"workflows": true,
	}
	explicitCancelActionWords = map[string]bool{
		"clean":   true,
		"cleanup": true,
		"clear":   true,
		"close":   true,
		"end":     true,
		"exit":    true,
		"finish":  true,
		"quit":    true,
		"wrap":    true,
	}
	explicitCancelContinuationWords = routeKeywordTokenSetForCancelSuffix(routeRules)
)

func explicitCancelMatches(prompt string, keywords []string) []routeKeywordMatch {
	words := routePromptWords(prompt)
	if len(words) == 0 {
		return nil
	}

	keywordSet := map[string]bool{}
	for _, keyword := range keywords {
		keywordSet[strings.ToLower(keyword)] = true
	}

	for index, word := range words {
		if !keywordSet[word.lower] {
			continue
		}
		clauseStart, clauseEnd := routeClauseWordBounds(prompt, words, index)
		clauseKeywordIndex := index - clauseStart
		clauseWords := words[clauseStart:clauseEnd]
		if !explicitCancelPrefixMatches(clauseWords[:clauseKeywordIndex]) {
			continue
		}
		if !explicitCancelSuffixMatches(clauseWords[clauseKeywordIndex+1:]) {
			continue
		}

		match := words[index]
		return []routeKeywordMatch{{
			start:   match.start,
			end:     match.end,
			trigger: prompt[match.start:match.end],
		}}
	}

	return nil
}

func explicitCancelPrefixMatches(words []routeWord) bool {
	index := consumeExplicitCancelPrefixNoise(words, 0)
	if index == len(words) {
		return true
	}

	for _, phrase := range explicitCancelLeadInPhrases {
		if routeWordsMatchPhrase(words[index:], phrase) != len(words[index:]) {
			continue
		}
		return true
	}

	return false
}

func consumeExplicitCancelPrefixNoise(words []routeWord, index int) int {
	for index < len(words) {
		lower := words[index].lower
		if !explicitCancelCourtesyWords[lower] && !explicitCancelAcknowledgementWords[lower] {
			break
		}
		index++
	}
	return index
}

func routeWordsMatchPhrase(words []routeWord, phrase []string) int {
	index := 0
	for _, part := range phrase {
		index = consumeExplicitCancelPrefixNoise(words, index)
		if index >= len(words) || words[index].lower != part {
			return -1
		}
		index++
	}
	return consumeExplicitCancelPrefixNoise(words, index)
}

func explicitCancelSuffixMatches(words []routeWord) bool {
	for _, word := range words {
		if explicitCancelConditionalWords[word.lower] {
			return false
		}
	}
	for _, word := range words {
		switch lower := word.lower; {
		case explicitCancelSuffixSkippableWords[lower]:
			continue
		case explicitCancelTargetIntroWords[lower]:
			return true
		case explicitCancelTargetPronouns[lower]:
			return true
		case explicitCancelBareObjectWords[lower]:
			return true
		case explicitCancelActionWords[lower]:
			return true
		case explicitCancelContinuationWords[lower]:
			return true
		default:
			return false
		}
	}
	return true
}

func routeClauseWordBounds(prompt string, words []routeWord, index int) (int, int) {
	start := index
	for start > 0 {
		if hasRouteClauseBoundary(prompt[words[start-1].end:words[start].start]) {
			break
		}
		start--
	}

	end := index + 1
	for end < len(words) {
		if hasRouteClauseBoundary(prompt[words[end-1].end:words[end].start]) {
			break
		}
		end++
	}
	return start, end
}

func hasRouteClauseBoundary(segment string) bool {
	for _, r := range segment {
		if isRouteClauseBoundaryRune(r) {
			return true
		}
	}
	return false
}

func isRouteClauseBoundaryRune(r rune) bool {
	switch r {
	case ',', '.', ';', ':', '!', '?', '\n', '\r':
		return true
	default:
		return false
	}
}

func routeKeywordTokenSetForCancelSuffix(rules []routeRule) map[string]bool {
	tokens := map[string]bool{}
	for _, rule := range rules {
		if rule.Skill == "cancel" {
			continue
		}
		for _, keyword := range rule.Keywords {
			for _, word := range routePromptWords(keyword) {
				if word.lower == "" {
					continue
				}
				tokens[word.lower] = true
			}
		}
	}
	return tokens
}

func routePromptWords(prompt string) []routeWord {
	words := []routeWord{}
	for index := 0; index < len(prompt); {
		r, size := utf8.DecodeRuneInString(prompt[index:])
		if !isRouteTokenRune(r) {
			index += size
			continue
		}

		start := index
		index += size
		for index < len(prompt) {
			r, size = utf8.DecodeRuneInString(prompt[index:])
			if !isRouteTokenRune(r) {
				break
			}
			index += size
		}
		words = append(words, routeWord{
			start: start,
			end:   index,
			lower: strings.ToLower(prompt[start:index]),
		})
	}
	return words
}

func keywordPattern(keyword string) (*regexp.Regexp, error) {
	return regexp.Compile(`(?i)` + regexp.QuoteMeta(keyword))
}

func hasKeywordBoundaries(prompt string, start int, end int) bool {
	// Check delimiters without consuming them so adjacent matches that share a
	// delimiter, such as "tdd tdd", are still reported independently.
	if start > 0 {
		r, _ := utf8.DecodeLastRuneInString(prompt[:start])
		if isRouteTokenRune(r) {
			return false
		}
	}
	if end < len(prompt) {
		r, _ := utf8.DecodeRuneInString(prompt[end:])
		if isRouteTokenRune(r) {
			return false
		}
	}
	return true
}

func isRouteTokenRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r)
}

func firstPromptInvocation(prompt string) string {
	match := promptInvocationPattern.FindStringSubmatchIndex(prompt)
	if len(match) == 0 {
		return ""
	}
	return prompt[match[4]:match[5]]
}

func sortIgnoredTriggers(ignored []routeIgnoredTrigger) {
	sort.SliceStable(ignored, func(i, j int) bool {
		if ignored[i].Start != ignored[j].Start {
			return ignored[i].Start < ignored[j].Start
		}
		if ignored[i].Skill != ignored[j].Skill {
			return ignored[i].Skill < ignored[j].Skill
		}
		return ignored[i].Trigger < ignored[j].Trigger
	})
}

func routeRuntimePath(skill string) string {
	return filepath.Join(CodexHome(), "skills", skill, "RUNTIME.md")
}

func routeRuntimeDocResolver() func(string) routeDoc {
	return func(skill string) routeDoc {
		path := routeRuntimePath(skill)
		return routeDoc{
			Path:       path,
			ActualPath: path,
			Label:      routeDocLabelRuntime,
		}
	}
}

func routeDocResolver(cwd string) func(string) routeDoc {
	return routeDocResolverForBase(routeDocBaseForCWD(cwd))
}

func routeDocResolverForBase(base routeDocBase) func(string) routeDoc {
	return func(skill string) routeDoc {
		if !isKnownRouteSkill(skill) {
			if doc, ok := installedRouteSkillDoc(base, skill); ok {
				return doc
			}
		}
		return routeDoc{
			Path:       base.displayDocPath(skill, "RUNTIME.md"),
			ActualPath: base.actualDocPath(skill, "RUNTIME.md"),
			Label:      routeDocLabelRuntime,
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

func (base routeDocBase) actualDocPath(skill string, filename string) string {
	return filepath.Join(base.actualSkillsDir, skill, filename)
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
	if len(preview.IgnoredTriggers) > 0 {
		fmt.Fprintln(&builder, "Ignored triggers:")
		for index, ignored := range preview.IgnoredTriggers {
			if ignored.Skill == "" {
				fmt.Fprintf(&builder, "  %d. %s %q\n", index+1, ignored.Source, ignored.Trigger)
			} else {
				fmt.Fprintf(&builder, "  %d. $%s\n", index+1, ignored.Skill)
				fmt.Fprintf(&builder, "     source: %s %q\n", ignored.Source, ignored.Trigger)
			}
			fmt.Fprintf(&builder, "     why: %s\n", ignored.Reason)
		}
	}
	if preview.ImplicitSuppressedBy != "" {
		fmt.Fprintf(&builder, "Implicit keywords: suppressed by %s\n", preview.ImplicitSuppressedBy)
	}
	fmt.Fprintln(&builder, "Rules: explicit $name invocations run first; implicit keywords are case-insensitive token-boundary matches, except $cancel which requires an explicit user cancellation request. Internal stop/completion guidance does not count.")
	return builder.String()
}

func routeActivationWhy(activation routeActivation) string {
	switch activation.Source {
	case routeSourceExplicitInvocation:
		return "explicit $name invocations run left-to-right before implicit keyword routing"
	case routeSourceImplicitKeyword:
		if activation.Skill == "cancel" {
			return "explicit user cancellation request matched a cancel trigger"
		}
		return "case-insensitive keyword match anywhere in the prompt on token boundaries"
	default:
		return activation.Source
	}
}

func routeActivationMode(activation routeActivation) string {
	switch activation.Source {
	case routeSourceExplicitInvocation:
		return "explicit"
	case routeSourceImplicitKeyword:
		return "implicit"
	default:
		return "unknown"
	}
}
