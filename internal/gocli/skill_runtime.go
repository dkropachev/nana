package gocli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type loadedSkillRuntimeDoc struct {
	Skill                string
	Label                string
	DisplayPath          string
	ActualPath           string
	Content              string
	CacheStatus          string
	ActivationSource     string
	ActivationMode       string
	MatchedKeyword       string
	SourceRule           string
	ImplicitSuppressedBy string
	Size                 int64
	ModTime              time.Time
}

type skillRuntimeDocCache struct {
	mu              sync.Mutex
	entries         map[string]skillRuntimeDocCacheEntry
	stat            func(string) (os.FileInfo, error)
	readFile        func(string) ([]byte, error)
	appendTelemetry func(skillRuntimeDocTelemetry)
}

type skillRuntimeDocCacheEntry struct {
	Path    string
	Content string
	Size    int64
	ModTime time.Time
}

type skillRuntimeDocTelemetry struct {
	Skill                string
	Path                 string
	Label                string
	CacheStatus          string
	ActivationSource     string
	ActivationMode       string
	MatchedKeyword       string
	SourceRule           string
	ImplicitSuppressedBy string
	ImplicitSuppressed   bool
	Size                 int64
	ModTime              time.Time
}

type contextTelemetryScope struct {
	RunID           string
	TurnID          string
	GeneratedRunID  bool
	GeneratedTurnID bool
}

type skillRuntimeActivationDecision struct {
	ActivationSource     string
	ActivationMode       string
	MatchedKeyword       string
	SourceRule           string
	ImplicitSuppressedBy string
}

var defaultSkillRuntimeDocCache = newSkillRuntimeDocCache()

func newSkillRuntimeDocCache() *skillRuntimeDocCache {
	return &skillRuntimeDocCache{
		entries:  map[string]skillRuntimeDocCacheEntry{},
		stat:     os.Stat,
		readFile: os.ReadFile,
	}
}

func loadActivatedSkillRuntimeDocs(cwd string, prompt string, codexHome ...string) ([]loadedSkillRuntimeDoc, error) {
	return loadActivatedSkillRuntimeDocsWithTelemetryScope(cwd, prompt, contextTelemetryScope{}, codexHome...)
}

func loadActivatedSkillRuntimeDocsWithTelemetryScope(cwd string, prompt string, scope contextTelemetryScope, codexHome ...string) ([]loadedSkillRuntimeDoc, error) {
	return loadActivatedSkillRuntimeDocsWithCacheAndTelemetryScope(cwd, prompt, defaultSkillRuntimeDocCache, scope, codexHome...)
}

func loadActivatedSkillRuntimeDocsWithCache(cwd string, prompt string, cache *skillRuntimeDocCache, codexHome ...string) ([]loadedSkillRuntimeDoc, error) {
	return loadActivatedSkillRuntimeDocsWithCacheAndTelemetryScope(cwd, prompt, cache, contextTelemetryScope{}, codexHome...)
}

func loadActivatedSkillRuntimeDocsWithCacheAndTelemetryScope(cwd string, prompt string, cache *skillRuntimeDocCache, scope contextTelemetryScope, codexHome ...string) ([]loadedSkillRuntimeDoc, error) {
	if strings.TrimSpace(prompt) == "" {
		return nil, nil
	}
	if cache == nil {
		cache = defaultSkillRuntimeDocCache
	}
	preview := ExplainPromptRouteForCWDAndCodexHome(cwd, firstString(codexHome...), prompt)
	if len(preview.Activations) == 0 {
		return nil, nil
	}

	docs := make([]loadedSkillRuntimeDoc, 0, len(preview.Activations))
	for _, activation := range preview.Activations {
		actualPath := strings.TrimSpace(activation.RuntimeActualPath)
		if actualPath == "" {
			actualPath = strings.TrimSpace(activation.RuntimePath)
		}
		if actualPath == "" {
			continue
		}
		decision := skillRuntimeActivationDecisionForRoute(activation, preview.ImplicitSuppressedBy)
		doc, ok, err := cache.load(cwd, activation.Skill, activation.DocLabel, activation.RuntimePath, actualPath, decision, scope)
		if err != nil {
			return nil, err
		}
		if ok {
			doc = annotateLoadedSkillRuntimeDoc(doc, decision)
			docs = append(docs, doc)
		}
	}
	return docs, nil
}

func firstString(values ...string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func (cache *skillRuntimeDocCache) load(cwd string, skill string, label string, displayPath string, actualPath string, decision skillRuntimeActivationDecision, scope contextTelemetryScope) (loadedSkillRuntimeDoc, bool, error) {
	if cache == nil {
		cache = defaultSkillRuntimeDocCache
	}
	cleanPath := cleanSkillRuntimePath(actualPath)
	info, err := cache.stat(cleanPath)
	if err != nil {
		if os.IsNotExist(err) {
			return loadedSkillRuntimeDoc{}, false, nil
		}
		return loadedSkillRuntimeDoc{}, false, err
	}
	if info.IsDir() {
		return loadedSkillRuntimeDoc{}, false, fmt.Errorf("skill runtime doc path is a directory: %s", cleanPath)
	}
	key := skillRuntimeCacheKey(cleanPath, info)

	cache.mu.Lock()
	if entry, ok := cache.entries[key]; ok {
		cache.mu.Unlock()
		doc := loadedSkillRuntimeDoc{
			Skill:       skill,
			Label:       label,
			DisplayPath: defaultString(strings.TrimSpace(displayPath), cleanPath),
			ActualPath:  cleanPath,
			Content:     entry.Content,
			CacheStatus: "hit",
			Size:        entry.Size,
			ModTime:     entry.ModTime,
		}
		cache.emitTelemetry(cwd, skillRuntimeDocTelemetry{
			Skill:                skill,
			Path:                 cleanPath,
			Label:                label,
			CacheStatus:          "hit",
			ActivationSource:     decision.ActivationSource,
			ActivationMode:       decision.ActivationMode,
			MatchedKeyword:       decision.MatchedKeyword,
			SourceRule:           decision.SourceRule,
			ImplicitSuppressedBy: decision.ImplicitSuppressedBy,
			ImplicitSuppressed:   decision.ImplicitSuppressedBy != "",
			Size:                 entry.Size,
			ModTime:              entry.ModTime,
		}, scope)
		return doc, true, nil
	}
	cache.mu.Unlock()

	contentBytes, err := cache.readFile(cleanPath)
	if err != nil {
		if os.IsNotExist(err) {
			return loadedSkillRuntimeDoc{}, false, nil
		}
		return loadedSkillRuntimeDoc{}, false, err
	}
	entry := skillRuntimeDocCacheEntry{
		Path:    cleanPath,
		Content: string(contentBytes),
		Size:    info.Size(),
		ModTime: info.ModTime(),
	}

	cache.mu.Lock()
	for existingKey, existing := range cache.entries {
		if existing.Path == cleanPath && existingKey != key {
			delete(cache.entries, existingKey)
		}
	}
	cache.entries[key] = entry
	cache.mu.Unlock()

	doc := loadedSkillRuntimeDoc{
		Skill:       skill,
		Label:       label,
		DisplayPath: defaultString(strings.TrimSpace(displayPath), cleanPath),
		ActualPath:  cleanPath,
		Content:     entry.Content,
		CacheStatus: "miss",
		Size:        entry.Size,
		ModTime:     entry.ModTime,
	}
	cache.emitTelemetry(cwd, skillRuntimeDocTelemetry{
		Skill:                skill,
		Path:                 cleanPath,
		Label:                label,
		CacheStatus:          "miss",
		ActivationSource:     decision.ActivationSource,
		ActivationMode:       decision.ActivationMode,
		MatchedKeyword:       decision.MatchedKeyword,
		SourceRule:           decision.SourceRule,
		ImplicitSuppressedBy: decision.ImplicitSuppressedBy,
		ImplicitSuppressed:   decision.ImplicitSuppressedBy != "",
		Size:                 entry.Size,
		ModTime:              entry.ModTime,
	}, scope)
	return doc, true, nil
}

func skillRuntimeActivationDecisionForRoute(activation routeActivation, implicitSuppressedBy string) skillRuntimeActivationDecision {
	return skillRuntimeActivationDecision{
		ActivationSource:     activation.Source,
		ActivationMode:       routeActivationMode(activation),
		MatchedKeyword:       activation.Trigger,
		SourceRule:           routeActivationWhy(activation),
		ImplicitSuppressedBy: strings.TrimSpace(implicitSuppressedBy),
	}
}

func annotateLoadedSkillRuntimeDoc(doc loadedSkillRuntimeDoc, decision skillRuntimeActivationDecision) loadedSkillRuntimeDoc {
	doc.ActivationSource = decision.ActivationSource
	doc.ActivationMode = decision.ActivationMode
	doc.MatchedKeyword = decision.MatchedKeyword
	doc.SourceRule = decision.SourceRule
	doc.ImplicitSuppressedBy = decision.ImplicitSuppressedBy
	return doc
}

func cleanSkillRuntimePath(path string) string {
	clean := filepath.Clean(strings.TrimSpace(path))
	if abs, err := filepath.Abs(clean); err == nil {
		return abs
	}
	return clean
}

func skillRuntimeCacheKey(path string, info os.FileInfo) string {
	return path + "\x00" + info.ModTime().UTC().Format(time.RFC3339Nano) + fmt.Sprintf("\x00%d", info.Size())
}

func (cache *skillRuntimeDocCache) emitTelemetry(cwd string, event skillRuntimeDocTelemetry, scope contextTelemetryScope) {
	if cache != nil && cache.appendTelemetry != nil {
		cache.appendTelemetry(event)
		return
	}
	appendSkillRuntimeDocTelemetryWithScope(cwd, event, scope)
}

func appendSkillRuntimeDocTelemetry(cwd string, event skillRuntimeDocTelemetry) {
	appendSkillRuntimeDocTelemetryWithScope(cwd, event, contextTelemetryScope{})
}

func appendSkillRuntimeDocTelemetryWithScope(cwd string, event skillRuntimeDocTelemetry, scope contextTelemetryScope) {
	appendContextTelemetryWithScope(cwd, map[string]any{
		"event":                  "skill_doc_load",
		"skill":                  event.Skill,
		"path":                   event.Path,
		"doc_label":              event.Label,
		"cache":                  event.CacheStatus,
		"matched_keyword":        event.MatchedKeyword,
		"activation_mode":        event.ActivationMode,
		"activation_source":      event.ActivationSource,
		"source_rule":            event.SourceRule,
		"implicit_suppressed":    event.ImplicitSuppressed,
		"implicit_suppressed_by": event.ImplicitSuppressedBy,
		"size_bytes":             event.Size,
		"mtime":                  event.ModTime.UTC().Format(time.RFC3339Nano),
		"loader":                 "nana_skill_runtime_cache",
		"schema":                 "skill_doc_load.v1",
	}, scope)
}

func appendContextTelemetry(cwd string, event map[string]any) {
	appendContextTelemetryWithScope(cwd, event, contextTelemetryScope{})
}

func appendContextTelemetryWithScope(cwd string, event map[string]any, scope contextTelemetryScope) {
	if contextTelemetryDisabled() {
		return
	}
	path := resolveContextTelemetryLogPath(cwd)
	if strings.TrimSpace(path) == "" {
		return
	}
	payload := map[string]any{}
	for key, value := range event {
		payload[key] = value
	}
	if _, ok := payload["timestamp"]; !ok {
		payload["timestamp"] = ISOTimeNow()
	}
	if _, ok := payload["run_id"]; !ok {
		if runID := firstNonEmptyString(strings.TrimSpace(scope.RunID), currentContextTelemetryRunID()); runID != "" {
			payload["run_id"] = runID
		}
	}
	if _, ok := payload["turn_id"]; !ok {
		if turnID := firstNonEmptyString(strings.TrimSpace(scope.TurnID), currentContextTelemetryTurnID()); turnID != "" {
			payload["turn_id"] = turnID
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer file.Close()
	_ = json.NewEncoder(file).Encode(payload)
}

func contextTelemetryDisabled() bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv("NANA_CONTEXT_TELEMETRY")))
	return value == "0" || value == "false" || value == "off"
}

func currentContextTelemetryTurnID() string {
	for _, key := range []string{"NANA_CONTEXT_TELEMETRY_TURN_ID", "NANA_TURN_ID", "CODEX_TURN_ID"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func formatLoadedSkillRuntimeDocs(docs []loadedSkillRuntimeDoc) string {
	if len(docs) == 0 {
		return ""
	}
	parts := []string{
		"<!-- NANA:SKILL_RUNTIME_DOCS:START -->",
		"<invoked_skill_runtime_docs>",
		"Loaded once for activated skills in this session; reuse these contents instead of re-reading unchanged runtime files.",
	}
	added := 0
	for _, doc := range docs {
		content := strings.TrimSpace(doc.Content)
		if content == "" {
			continue
		}
		label := defaultString(strings.TrimSpace(doc.Label), routeDocLabelRuntime)
		path := defaultString(strings.TrimSpace(doc.DisplayPath), doc.ActualPath)
		activationSource := defaultString(strings.TrimSpace(doc.ActivationSource), "unknown")
		activationMode := defaultString(strings.TrimSpace(doc.ActivationMode), "unknown")
		matchedKeyword := strings.TrimSpace(doc.MatchedKeyword)
		sourceRule := strings.TrimSpace(doc.SourceRule)
		implicitSuppressedBy := strings.TrimSpace(doc.ImplicitSuppressedBy)
		parts = append(parts,
			fmt.Sprintf("<skill name=%q doc=%q path=%q cache=%q matched_keyword=%q activation_source=%q activation_mode=%q source_rule=%q implicit_suppressed=%q implicit_suppressed_by=%q>", doc.Skill, label, path, doc.CacheStatus, matchedKeyword, activationSource, activationMode, sourceRule, fmt.Sprint(implicitSuppressedBy != ""), implicitSuppressedBy),
			content,
			"</skill>",
		)
		added++
	}
	if added == 0 {
		return ""
	}
	parts = append(parts, "</invoked_skill_runtime_docs>", "<!-- NANA:SKILL_RUNTIME_DOCS:END -->")
	return strings.Join(parts, "\n")
}
