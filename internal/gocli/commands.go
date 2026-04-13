package gocli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

var reasoningModes = map[string]bool{
	"low": true, "medium": true, "high": true, "xhigh": true,
}

const ReasoningKey = "model_reasoning_effort"
const ReasoningUsage = "Usage: nana reasoning [low|medium|high|xhigh]\nSets the current Codex config and NANA user-level default used by future `nana setup` runs."

type nanaUserConfig struct {
	Version                int    `json:"version"`
	DefaultReasoningEffort string `json:"default_reasoning_effort,omitempty"`
	UpdatedAt              string `json:"updated_at,omitempty"`
}

func Status(cwd string) error {
	refs, err := ListModeStateFilesWithScopePreference(cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[nana-go] status warning: %v\n", err)
		fmt.Fprintln(os.Stdout, "No active modes.")
		return nil
	}
	if len(refs) == 0 {
		fmt.Fprintln(os.Stdout, "No active modes.")
		return nil
	}
	for _, ref := range refs {
		content, err := os.ReadFile(ref.Path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[nana-go] status warning: %v\n", err)
			continue
		}
		var state map[string]any
		if err := json.Unmarshal(content, &state); err != nil {
			fmt.Fprintf(os.Stderr, "[nana-go] status warning: %v\n", err)
			continue
		}
		phase, _ := state["current_phase"].(string)
		if phase == "" {
			phase = "n/a"
		}
		status := "inactive"
		if active, ok := state["active"].(bool); ok && active {
			status = "ACTIVE"
		}
		fmt.Fprintf(os.Stdout, "%s: %s (phase: %s)\n", ref.Mode, status, phase)
	}
	return nil
}

func Cancel(cwd string) error {
	refs, err := ListModeStateFilesWithScopePreference(cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[nana-go] cancel warning: %v\n", err)
		fmt.Fprintln(os.Stdout, "No active modes to cancel.")
		return nil
	}

	type entry struct {
		ref   ModeStateFileRef
		state map[string]any
	}

	states := map[string]*entry{}
	for _, ref := range refs {
		content, err := os.ReadFile(ref.Path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[nana-go] cancel warning: %v\n", err)
			continue
		}
		var state map[string]any
		if err := json.Unmarshal(content, &state); err != nil {
			fmt.Fprintf(os.Stderr, "[nana-go] cancel warning: %v\n", err)
			continue
		}
		states[ref.Mode] = &entry{ref: ref, state: state}
	}

	changed := map[string]bool{}
	reported := []string{}
	cancelMode := func(mode string) {
		current, ok := states[mode]
		if !ok {
			return
		}
		active, _ := current.state["active"].(bool)
		if !active {
			return
		}
		now := ISOTimeNow()
		current.state["active"] = false
		current.state["current_phase"] = "cancelled"
		current.state["completed_at"] = now
		current.state["last_turn_at"] = now
		changed[mode] = true
		reported = append(reported, mode)
	}

	ralph, hasRalph := states["ralph"]
	hadActiveRalph := false
	if hasRalph {
		if active, _ := ralph.state["active"].(bool); active {
			hadActiveRalph = true
			cancelMode("ralph")
			if linked, _ := ralph.state["linked_ultrawork"].(bool); linked {
				cancelMode("ultrawork")
			} else if linkedMode, _ := ralph.state["linked_mode"].(string); linkedMode == "ultrawork" {
				cancelMode("ultrawork")
			}
		}
	}
	if !hadActiveRalph {
		for mode := range states {
			cancelMode(mode)
		}
	}

	for mode := range changed {
		current := states[mode]
		payload, err := json.MarshalIndent(current.state, "", "  ")
		if err != nil {
			return err
		}
		payload = append(payload, '\n')
		if err := os.WriteFile(current.ref.Path, payload, 0o644); err != nil {
			return err
		}
	}

	if len(reported) == 0 {
		fmt.Fprintln(os.Stdout, "No active modes to cancel.")
		return nil
	}
	for _, mode := range reported {
		fmt.Fprintf(os.Stdout, "Cancelled: %s\n", mode)
	}
	return nil
}

func Reasoning(args []string) error {
	configPath := CodexConfigPath()
	if len(args) == 0 {
		defaultMode := defaultNanaReasoningMode()
		content, err := os.ReadFile(configPath)
		if err != nil {
			fmt.Fprintf(os.Stdout, "%s is not set (%s does not exist).\n", ReasoningKey, configPath)
			fmt.Fprintf(os.Stdout, "NANA default %s: %s\n", ReasoningKey, defaultMode)
			fmt.Fprintln(os.Stdout, ReasoningUsage)
			return nil
		}
		if current := ReadTopLevelTomlString(string(content), ReasoningKey); current != "" {
			fmt.Fprintf(os.Stdout, "Current %s: %s\n", ReasoningKey, current)
			fmt.Fprintf(os.Stdout, "NANA default %s: %s\n", ReasoningKey, defaultMode)
			return nil
		}
		fmt.Fprintf(os.Stdout, "%s is not set in %s.\n", ReasoningKey, configPath)
		fmt.Fprintf(os.Stdout, "NANA default %s: %s\n", ReasoningKey, defaultMode)
		fmt.Fprintln(os.Stdout, ReasoningUsage)
		return nil
	}

	mode := args[0]
	if !reasoningModes[mode] {
		return fmt.Errorf("invalid reasoning mode %q. expected one of: low, medium, high, xhigh.\n%s", mode, ReasoningUsage)
	}

	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return err
	}
	content := ""
	if existing, err := os.ReadFile(configPath); err == nil {
		content = string(existing)
	}
	updated := UpsertTopLevelTomlString(content, ReasoningKey, mode)
	if err := os.WriteFile(configPath, []byte(updated), 0o644); err != nil {
		return err
	}
	if err := writeNanaReasoningDefault(mode); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Set %s=%q in %s\n", ReasoningKey, mode, configPath)
	fmt.Fprintf(os.Stdout, "Set NANA default %s=%q in %s\n", ReasoningKey, mode, nanaUserConfigPath())
	return nil
}

func defaultNanaReasoningMode() string {
	config, err := readNanaUserConfig()
	if err == nil && reasoningModes[config.DefaultReasoningEffort] {
		return config.DefaultReasoningEffort
	}
	return "xhigh"
}

func writeNanaReasoningDefault(mode string) error {
	config, _ := readNanaUserConfig()
	config.Version = 1
	config.DefaultReasoningEffort = mode
	config.UpdatedAt = ISOTimeNow()
	return writeGithubJSON(nanaUserConfigPath(), config)
}

func readNanaUserConfig() (nanaUserConfig, error) {
	var config nanaUserConfig
	if err := readGithubJSON(nanaUserConfigPath(), &config); err != nil {
		return nanaUserConfig{Version: 1}, err
	}
	if config.Version == 0 {
		config.Version = 1
	}
	return config, nil
}

func nanaUserConfigPath() string {
	return filepath.Join(githubNanaHome(), "config.json")
}
