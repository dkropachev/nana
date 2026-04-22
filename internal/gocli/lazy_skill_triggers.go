package gocli

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	_ "embed"
)

//go:embed lazy_skill_triggers.json
var lazySkillTriggersManifestJSON []byte

const (
	lazySkillTriggersMarker     = "<!-- NANA:T internal/gocli/lazy_skill_triggers.json -->"
	lazySkillTriggersEndHeading = "\n## Execution and Verification"
)

var lazySkillNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

type lazySkillTriggerManifest struct {
	SchemaVersion int                `json:"schema_version"`
	Skills        []lazySkillTrigger `json:"skills"`
}

type lazySkillTrigger struct {
	Skill     string   `json:"skill"`
	Triggers  []string `json:"triggers"`
	Note      string   `json:"note,omitempty"`
	MatchMode string   `json:"match_mode,omitempty"`
}

func lazySkillTriggerEntries() ([]lazySkillTrigger, error) {
	var manifest lazySkillTriggerManifest
	if err := json.Unmarshal(lazySkillTriggersManifestJSON, &manifest); err != nil {
		return nil, fmt.Errorf("parse lazy skill trigger manifest: %w", err)
	}
	if manifest.SchemaVersion != 1 {
		return nil, fmt.Errorf("unsupported lazy skill trigger manifest schema_version %d", manifest.SchemaVersion)
	}
	if len(manifest.Skills) == 0 {
		return nil, fmt.Errorf("lazy skill trigger manifest has no skills")
	}

	seenSkills := map[string]bool{}
	seenTriggerOwners := map[string]string{}
	for index, entry := range manifest.Skills {
		if !lazySkillNamePattern.MatchString(entry.Skill) {
			return nil, fmt.Errorf("lazy skill trigger manifest skill %d has invalid name %q", index, entry.Skill)
		}
		if seenSkills[entry.Skill] {
			return nil, fmt.Errorf("lazy skill trigger manifest duplicates skill %q", entry.Skill)
		}
		seenSkills[entry.Skill] = true
		if len(entry.Triggers) == 0 {
			return nil, fmt.Errorf("lazy skill trigger manifest skill %q has no triggers", entry.Skill)
		}
		if _, err := parseRouteMatchMode(entry.MatchMode); err != nil {
			return nil, fmt.Errorf("lazy skill trigger manifest skill %q: %w", entry.Skill, err)
		}
		seenTriggers := map[string]bool{}
		for triggerIndex, trigger := range entry.Triggers {
			if strings.TrimSpace(trigger) != trigger || trigger == "" {
				return nil, fmt.Errorf("lazy skill trigger manifest skill %q trigger %d is blank or padded", entry.Skill, triggerIndex)
			}
			if strings.Contains(trigger, "`") {
				return nil, fmt.Errorf("lazy skill trigger manifest skill %q trigger %q contains a backtick", entry.Skill, trigger)
			}
			folded := strings.ToLower(trigger)
			if seenTriggers[folded] {
				return nil, fmt.Errorf("lazy skill trigger manifest skill %q duplicates trigger %q", entry.Skill, trigger)
			}
			seenTriggers[folded] = true
			if owner := seenTriggerOwners[folded]; owner != "" {
				return nil, fmt.Errorf("lazy skill trigger manifest trigger %q is assigned to both %q and %q", trigger, owner, entry.Skill)
			}
			seenTriggerOwners[folded] = entry.Skill
		}
	}
	return cloneLazySkillTriggers(manifest.Skills), nil
}

func mustLazySkillTriggerEntries() []lazySkillTrigger {
	entries, err := lazySkillTriggerEntries()
	if err != nil {
		panic(err)
	}
	return entries
}

func lazySkillTriggerRouteRules() []routeRule {
	entries := mustLazySkillTriggerEntries()
	rules := make([]routeRule, 0, len(entries))
	for _, entry := range entries {
		matchMode, err := parseRouteMatchMode(entry.MatchMode)
		if err != nil {
			panic(err)
		}
		rules = append(rules, routeRule{
			Skill:     entry.Skill,
			Keywords:  append([]string(nil), entry.Triggers...),
			MatchMode: matchMode,
		})
	}
	return rules
}

func renderLazySkillTriggersBlock(skillsBase string) string {
	return lazySkillTriggersMarker + "\n" + renderLazySkillTriggerRows(skillsBase)
}

func renderLazySkillTriggerRows(skillsBase string) string {
	skillsBase = strings.TrimRight(skillsBase, "/")
	var builder strings.Builder
	for _, entry := range mustLazySkillTriggerEntries() {
		fmt.Fprintf(&builder, "- `$%s` (`%s/%s/RUNTIME.md`): %s", entry.Skill, skillsBase, entry.Skill, renderBacktickList(entry.Triggers))
		if strings.TrimSpace(entry.Note) != "" {
			fmt.Fprintf(&builder, "; %s", strings.TrimSpace(entry.Note))
		}
		builder.WriteByte('\n')
	}
	return builder.String()
}

func renderBacktickList(values []string) string {
	rendered := make([]string, 0, len(values))
	for _, value := range values {
		rendered = append(rendered, "`"+value+"`")
	}
	return strings.Join(rendered, ", ")
}

func lazySkillTriggerNote(skill string) string {
	for _, entry := range mustLazySkillTriggerEntries() {
		if entry.Skill == skill {
			return strings.TrimSpace(entry.Note)
		}
	}
	return ""
}

func renderAgentsLazySkillTriggers(content string) string {
	return replaceAgentsLazySkillTriggers(content, "~/.codex/skills")
}

func replaceAgentsLazySkillTriggers(content string, skillsBase string) string {
	start := strings.Index(content, lazySkillTriggersMarker)
	if start < 0 {
		return content
	}
	end := strings.Index(content[start:], lazySkillTriggersEndHeading)
	if end < 0 {
		return content
	}
	end += start
	return content[:start] + renderLazySkillTriggersBlock(skillsBase) + content[end:]
}

func cloneLazySkillTriggers(entries []lazySkillTrigger) []lazySkillTrigger {
	cloned := make([]lazySkillTrigger, 0, len(entries))
	for _, entry := range entries {
		entry.Triggers = append([]string(nil), entry.Triggers...)
		cloned = append(cloned, entry)
	}
	return cloned
}
