package gocli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dkropachev/nana/internal/gocliassets"
)

type SetupOptions struct {
	Scope   string
	DryRun  bool
	Force   bool
	Verbose bool

	writeCache *setupWriteCache
	stats      *setupWriteStats
	timer      *setupPhaseTimer
}

const setupWriteCacheVersion = 1

type setupWriteCache struct {
	Version int                        `json:"version"`
	Entries map[string]setupCacheEntry `json:"entries"`

	path  string
	dirty bool
}

type setupCacheEntry struct {
	Checksum        string `json:"checksum"`
	Size            int64  `json:"size"`
	ModTimeUnixNano int64  `json:"mod_time_unix_nano"`
}

type setupWriteStats struct {
	Created   int
	Updated   int
	Unchanged int
}

type setupPhaseTimer struct {
	enabled bool
	phases  []setupPhaseTiming
	started time.Time
}

type setupPhaseTiming struct {
	Name     string
	Duration time.Duration
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

	options.timer = newSetupPhaseTimer(options.Verbose)
	options.stats = &setupWriteStats{}
	if !options.DryRun {
		options.writeCache = loadSetupWriteCache(setupWriteCachePath(cwd))
	}
	scopeDirs := resolveSetupScopeDirectories(cwd, options.Scope)
	if options.Scope == "user" {
		fmt.Fprintln(os.Stdout, "User scope leaves project AGENTS.md unchanged.")
	}

	if err := runSetupPhase(options, "install prompts", func() error {
		return installPrompts(repoRoot, scopeDirs.promptsDir, options)
	}); err != nil {
		return err
	}
	if err := runSetupPhase(options, "install skills", func() error {
		return installSkills(repoRoot, scopeDirs.skillsDir, options)
	}); err != nil {
		return err
	}
	if err := runSetupPhase(options, "install native agents", func() error {
		return installAgents(scopeDirs.nativeAgentsDir, options)
	}); err != nil {
		return err
	}
	if err := runSetupPhase(options, "ensure nana dirs", func() error {
		return ensureNanaDirectories(cwd, options)
	}); err != nil {
		return err
	}
	if err := runSetupPhase(options, "write config", func() error {
		return writeSetupConfig(scopeDirs.codexConfigFile, options)
	}); err != nil {
		return err
	}
	if err := runSetupPhase(options, "write AGENTS.md", func() error {
		return writeSetupAgentsMd(repoRoot, cwd, scopeDirs.codexHomeDir, options)
	}); err != nil {
		return err
	}
	if err := runSetupPhase(options, "install investigate home", func() error {
		return installInvestigateCodexHome(repoRoot, cwd, options.Scope, options, scopeDirs.codexHomeDir)
	}); err != nil {
		return err
	}
	if err := runSetupPhase(options, "persist setup scope", func() error {
		if !options.DryRun {
			if err := os.MkdirAll(filepath.Join(cwd, ".nana"), 0o755); err != nil {
				return err
			}
		}
		scopePath := filepath.Join(cwd, ".nana", "setup-scope.json")
		payload, _ := json.Marshal(map[string]string{"scope": options.Scope})
		if err := writeBytesIfChanged(scopePath, payload, options); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return err
	}
	if err := runSetupPhase(options, "persist setup cache", func() error {
		return options.writeCache.save()
	}); err != nil {
		return err
	}
	options.stats.printSummary(os.Stdout, options.DryRun)
	options.timer.printSummary(os.Stdout)
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
		options.stats.recordUnchanged()
		return nil
	}
	content, err := os.ReadFile(source)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return writeBytesIfMissing(target, content, options)
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
	for _, file := range []struct {
		path    string
		content string
	}{
		{path: filepath.Join(cwd, ".nana", "project-memory.json"), content: "{}\n"},
		{path: filepath.Join(cwd, ".nana", "notepad.md"), content: "# NANA Notepad\n\n"},
	} {
		if err := writeFileIfMissing(file.path, file.content, options); err != nil {
			return err
		}
	}
	return nil
}

func writeFileIfMissing(path string, content string, options SetupOptions) error {
	return writeBytesIfMissing(path, []byte(content), options)
}

func writeBytesIfMissing(path string, content []byte, options SetupOptions) error {
	info, err := os.Stat(path)
	if err == nil {
		if info.IsDir() {
			return fmt.Errorf("%s exists and is a directory", path)
		}
		options.stats.recordUnchanged()
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	options.stats.recordChanged("created")
	if options.DryRun {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, content, 0o644)
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
	targetPath := resolveManagedAgentsPath(resolveScopeForAgentsTarget(cwd, codexHomeDir), cwd, codexHomeDir)
	if filepath.Clean(targetPath) == filepath.Clean(filepath.Join(cwd, "AGENTS.md")) && fileExists(targetPath) && !options.Force {
		fmt.Fprintln(os.Stdout, "Skipped AGENTS.md overwrite")
		return nil
	}
	content, err := renderManagedAgentsContent(repoRoot, cwd, codexHomeDir, targetPath)
	if err != nil {
		return err
	}
	return writeFileIfChanged(targetPath, content, options)
}

func resolveScopeForAgentsTarget(cwd string, codexHomeDir string) string {
	if strings.Contains(codexHomeDir, filepath.Join(cwd, ".codex")) {
		return "project"
	}
	return "user"
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
	return writeBytesWithChecksumGuard(path, content, 0o644, options.DryRun, options.writeCache, options.stats)
}

func writeRuntimeBytesIfChanged(path string, content []byte) error {
	return writeBytesWithChecksumGuard(path, content, 0o644, false, nil, nil)
}

func writeBytesWithChecksumGuard(path string, content []byte, mode os.FileMode, dryRun bool, cache *setupWriteCache, stats *setupWriteStats) error {
	checksum := sha256BytesHex(content)
	if cache != nil && cache.matches(path, checksum) {
		stats.recordUnchanged()
		return nil
	}
	status := "created"
	if existing, err := os.ReadFile(path); err == nil {
		if sha256BytesHex(existing) == checksum {
			if cache != nil && !dryRun {
				cache.update(path, checksum)
			}
			stats.recordUnchanged()
			return nil
		}
		status = "updated"
	} else if !os.IsNotExist(err) {
		return err
	}
	if dryRun {
		stats.recordChanged(status)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, content, mode); err != nil {
		return err
	}
	if cache != nil {
		cache.update(path, checksum)
	}
	stats.recordChanged(status)
	return nil
}

func sha256BytesHex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
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

func setupWriteCachePath(cwd string) string {
	return filepath.Join(cwd, ".nana", "state", "setup-cache.json")
}

func loadSetupWriteCache(path string) *setupWriteCache {
	cache := &setupWriteCache{
		Version: setupWriteCacheVersion,
		Entries: map[string]setupCacheEntry{},
		path:    path,
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return cache
	}
	var persisted setupWriteCache
	if err := json.Unmarshal(content, &persisted); err != nil || persisted.Version != setupWriteCacheVersion {
		return cache
	}
	cache.Entries = persisted.Entries
	if cache.Entries == nil {
		cache.Entries = map[string]setupCacheEntry{}
	}
	return cache
}

func (c *setupWriteCache) matches(path string, checksum string) bool {
	if c == nil {
		return false
	}
	entry, ok := c.Entries[setupCacheKey(path)]
	if !ok || entry.Checksum != checksum {
		return false
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	if info.Size() != entry.Size || info.ModTime().UnixNano() != entry.ModTimeUnixNano {
		return false
	}
	// Size and mtime only make the cache entry plausible; content remains
	// authoritative because archive/sync tools can restore both metadata values.
	content, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return sha256BytesHex(content) == checksum
}

func (c *setupWriteCache) update(path string, checksum string) {
	if c == nil {
		return
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return
	}
	key := setupCacheKey(path)
	entry := setupCacheEntry{
		Checksum:        checksum,
		Size:            info.Size(),
		ModTimeUnixNano: info.ModTime().UnixNano(),
	}
	if c.Entries == nil {
		c.Entries = map[string]setupCacheEntry{}
	}
	if existing, ok := c.Entries[key]; ok && existing == entry {
		return
	}
	c.Entries[key] = entry
	c.dirty = true
}

func (c *setupWriteCache) save() error {
	if c == nil || !c.dirty {
		return nil
	}
	c.Version = setupWriteCacheVersion
	if c.Entries == nil {
		c.Entries = map[string]setupCacheEntry{}
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if existing, err := os.ReadFile(c.path); err == nil && bytes.Equal(existing, data) {
		c.dirty = false
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(c.path, data, 0o644); err != nil {
		return err
	}
	c.dirty = false
	return nil
}

func setupCacheKey(path string) string {
	return filepath.Clean(path)
}

func (s *setupWriteStats) recordUnchanged() {
	if s == nil {
		return
	}
	s.Unchanged++
}

func (s *setupWriteStats) recordChanged(status string) {
	if s == nil {
		return
	}
	switch status {
	case "created":
		s.Created++
	case "updated":
		s.Updated++
	}
}

func (s *setupWriteStats) printSummary(out *os.File, dryRun bool) {
	if s == nil {
		return
	}
	if dryRun {
		fmt.Fprintf(out, "Setup outputs: would_create=%d would_update=%d unchanged=%d\n", s.Created, s.Updated, s.Unchanged)
		return
	}
	fmt.Fprintf(out, "Setup outputs: created=%d updated=%d unchanged=%d\n", s.Created, s.Updated, s.Unchanged)
}

func newSetupPhaseTimer(enabled bool) *setupPhaseTimer {
	return &setupPhaseTimer{enabled: enabled, started: time.Now()}
}

func runSetupPhase(options SetupOptions, name string, fn func() error) error {
	if options.timer == nil || !options.timer.enabled {
		return fn()
	}
	start := time.Now()
	err := fn()
	options.timer.phases = append(options.timer.phases, setupPhaseTiming{
		Name:     name,
		Duration: time.Since(start),
	})
	return err
}

func (t *setupPhaseTimer) printSummary(out *os.File) {
	if t == nil || !t.enabled {
		return
	}
	total := time.Since(t.started)
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Setup timings:")
	for _, phase := range t.phases {
		fmt.Fprintf(out, "  %-24s %s\n", phase.Name+":", formatSetupDuration(phase.Duration))
	}
	fmt.Fprintf(out, "  %-24s %s\n", "total:", formatSetupDuration(total))
}

func formatSetupDuration(duration time.Duration) string {
	if duration < time.Millisecond {
		return duration.String()
	}
	return duration.Round(time.Millisecond).String()
}
