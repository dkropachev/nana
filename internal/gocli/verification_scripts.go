package gocli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func writeVerificationScripts(runtimeName string, sandboxPath string, repoCheckoutPath string, plan githubVerificationPlan, refreshCommand []string) (string, error) {
	dir := filepath.Join(sandboxPath, ".nana", runtimeName, "verify")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	writeScript := func(name string, body []string) error {
		path := filepath.Join(dir, name)
		return os.WriteFile(path, []byte(strings.Join(body, "\n")+"\n"), 0o755)
	}
	buildCommandScript := func(commands []string, emptyMessage string) []string {
		lines := []string{
			"#!/usr/bin/env bash",
			"set -euo pipefail",
			fmt.Sprintf("cd %q", repoCheckoutPath),
		}
		if len(commands) == 0 {
			lines = append(lines, fmt.Sprintf("echo %q", emptyMessage))
		} else {
			lines = append(lines, commands...)
		}
		return lines
	}
	if err := writeScript("lint.sh", buildCommandScript(plan.Lint, "No lint command detected for this repo.")); err != nil {
		return "", err
	}
	if err := writeScript("compile.sh", buildCommandScript(plan.Compile, "No compile command detected for this repo.")); err != nil {
		return "", err
	}
	if err := writeScript("unit-tests.sh", buildCommandScript(plan.Unit, "No unit-test command detected for this repo.")); err != nil {
		return "", err
	}
	if err := writeScript("integration-tests.sh", buildCommandScript(plan.Integration, "No integration-test command detected for this repo.")); err != nil {
		return "", err
	}
	if err := writeScript("benchmark-tests.sh", buildCommandScript(plan.Benchmarks, "No benchmark command detected for this repo.")); err != nil {
		return "", err
	}
	refreshInvocation := shellJoinQuoted(refreshCommand)
	if err := writeScript("refresh.sh", []string{
		"#!/usr/bin/env bash",
		"set -euo pipefail",
		refreshInvocation,
	}); err != nil {
		return "", err
	}
	if err := writeScript("worker-done.sh", []string{
		"#!/usr/bin/env bash",
		"set -euo pipefail",
		fmt.Sprintf("DIR=%q", dir),
		"\"$DIR/refresh.sh\"",
		"\"$DIR/lint.sh\"",
		"\"$DIR/compile.sh\"",
		"\"$DIR/unit-tests.sh\"",
	}); err != nil {
		return "", err
	}
	if err := writeScript("all.sh", []string{
		"#!/usr/bin/env bash",
		"set -euo pipefail",
		fmt.Sprintf("DIR=%q", dir),
		"\"$DIR/refresh.sh\"",
		"\"$DIR/lint.sh\"",
		"\"$DIR/compile.sh\"",
		"\"$DIR/unit-tests.sh\"",
		"\"$DIR/integration-tests.sh\"",
	}); err != nil {
		return "", err
	}
	return dir, nil
}

func shellJoinQuoted(parts []string) string {
	quoted := make([]string, 0, len(parts))
	for _, part := range parts {
		quoted = append(quoted, shellQuote(part))
	}
	return strings.Join(quoted, " ")
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
