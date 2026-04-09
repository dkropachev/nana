package gocli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseReflectArgs(t *testing.T) {
	parsed, err := ParseReflectArgs([]string{"--prompt", "find auth"})
	if err != nil {
		t.Fatalf("ParseReflectArgs(): %v", err)
	}
	if parsed.Prompt != "find auth" {
		t.Fatalf("unexpected prompt: %q", parsed.Prompt)
	}
}

func TestLoadReflectPromptFromFile(t *testing.T) {
	wd := t.TempDir()
	path := filepath.Join(wd, "prompt.txt")
	if err := os.WriteFile(path, []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
	prompt, err := LoadReflectPrompt(ParsedReflectArgs{PromptFile: path})
	if err != nil {
		t.Fatalf("LoadReflectPrompt(): %v", err)
	}
	if prompt != "hello" {
		t.Fatalf("unexpected prompt: %q", prompt)
	}
}

func TestReflectUsesSiblingHarnessAndEmbeddedPromptWithoutRepoRoot(t *testing.T) {
	wd := t.TempDir()
	binDir := filepath.Join(wd, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	harnessPath := filepath.Join(binDir, binaryName("nana-explore-harness"))
	stub := strings.Join([]string{
		"#!/bin/sh",
		"printf 'harness:%s\\n' \"$*\"",
	}, "\n")
	if err := os.WriteFile(harnessPath, []byte(stub), 0o755); err != nil {
		t.Fatalf("write harness: %v", err)
	}

	originalExe := os.Args[0]
	os.Args[0] = filepath.Join(binDir, "nana")
	defer func() { os.Args[0] = originalExe }()

	output, err := captureStdout(t, func() error {
		return Reflect("", wd, []string{"--prompt", "find auth"})
	})
	if err != nil {
		t.Fatalf("Reflect(): %v", err)
	}
	if !strings.Contains(output, "--prompt find auth") || !strings.Contains(output, "--prompt-file") {
		t.Fatalf("unexpected harness output: %q", output)
	}
}
