package gocli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type routeBenchmarkPromptCase struct {
	name        string
	prompt      string
	activations int
}

var (
	routeBenchmarkPromptCases = []routeBenchmarkPromptCase{
		{
			name:        "zero-match",
			prompt:      "Summarize this repository status without activating a skill.",
			activations: 0,
		},
		{
			name:        "one-match",
			prompt:      "Please ANALYZE this failure.",
			activations: 1,
		},
		{
			name:        "multi-match",
			prompt:      "Please analyze, then fix build failures in parallel.",
			activations: 3,
		},
	}

	routeBenchmarkPreviewSink  routePreview
	routeBenchmarkDocBytesSink int
)

func BenchmarkRouteExplainRepresentativePrompts(b *testing.B) {
	for _, tc := range routeBenchmarkPromptCases {
		tc := tc
		b.Run(tc.name+"/cold", func(b *testing.B) {
			base := b.TempDir()
			setRouteBenchmarkEnv := routeBenchmarkEnvSetter(b)
			args := []string{"--explain", tc.prompt}
			routeBenchmarkAssertActivations(b, filepath.Join(base, "assert-cwd"), filepath.Join(base, "assert-home"), "", tc)

			withBenchmarkStdoutDiscarded(b, func() {
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					b.StopTimer()
					cwd := filepath.Join(base, fmt.Sprintf("cold-cwd-%d", i))
					home := filepath.Join(base, fmt.Sprintf("cold-home-%d", i))
					setRouteBenchmarkEnv(home, "")
					b.StartTimer()
					if err := Route(cwd, args); err != nil {
						b.Fatal(err)
					}
				}
			})
		})

		b.Run(tc.name+"/warm", func(b *testing.B) {
			base := b.TempDir()
			cwd := filepath.Join(base, "warm-cwd")
			home := filepath.Join(base, "warm-home")
			setRouteBenchmarkEnv := routeBenchmarkEnvSetter(b)
			setRouteBenchmarkEnv(home, "")
			args := []string{"--explain", tc.prompt}
			routeBenchmarkAssertActivations(b, cwd, home, "", tc)

			withBenchmarkStdoutDiscarded(b, func() {
				if err := Route(cwd, args); err != nil {
					b.Fatal(err)
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if err := Route(cwd, args); err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}

func BenchmarkRouteTriggerMatching(b *testing.B) {
	for _, tc := range routeBenchmarkPromptCases {
		tc := tc
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				preview := ExplainPromptRoute(tc.prompt)
				if len(preview.Activations) != tc.activations {
					b.Fatalf("expected %d activations, got %#v", tc.activations, preview.Activations)
				}
				routeBenchmarkPreviewSink = preview
			}
		})
	}
}

func BenchmarkRouteFirstRuntimeDocLoad(b *testing.B) {
	const prompt = "Please analyze this failure."
	const skill = "analyze"

	b.Run("cold", func(b *testing.B) {
		base := b.TempDir()
		setRouteBenchmarkEnv := routeBenchmarkEnvSetter(b)
		b.ReportMetric(float64(len(routeRuntimeDocBenchmarkContent(skill))), "doc_bytes")
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			b.StopTimer()
			cwd := filepath.Join(base, fmt.Sprintf("cold-cwd-%d", i))
			home := filepath.Join(base, fmt.Sprintf("cold-home-%d", i))
			codexHome := filepath.Join(base, fmt.Sprintf("cold-codex-home-%d", i))
			writeRouteRuntimeDocBenchmarkFile(b, codexHome, skill)
			setRouteBenchmarkEnv(home, codexHome)
			b.StartTimer()

			preview := ExplainPromptRouteForCWD(cwd, prompt)
			docBytes, err := readFirstRouteRuntimeDoc(preview)
			if err != nil {
				b.Fatal(err)
			}
			routeBenchmarkDocBytesSink = len(docBytes)
		}
	})

	b.Run("warm", func(b *testing.B) {
		base := b.TempDir()
		cwd := filepath.Join(base, "warm-cwd")
		home := filepath.Join(base, "warm-home")
		codexHome := filepath.Join(base, "warm-codex-home")
		writeRouteRuntimeDocBenchmarkFile(b, codexHome, skill)
		setRouteBenchmarkEnv := routeBenchmarkEnvSetter(b)
		setRouteBenchmarkEnv(home, codexHome)

		preview := ExplainPromptRouteForCWD(cwd, prompt)
		docBytes, err := readFirstRouteRuntimeDoc(preview)
		if err != nil {
			b.Fatal(err)
		}
		b.ReportMetric(float64(len(docBytes)), "doc_bytes")
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			preview := ExplainPromptRouteForCWD(cwd, prompt)
			docBytes, err := readFirstRouteRuntimeDoc(preview)
			if err != nil {
				b.Fatal(err)
			}
			routeBenchmarkDocBytesSink = len(docBytes)
		}
	})
}

func routeBenchmarkAssertActivations(b *testing.B, cwd string, home string, codexHome string, tc routeBenchmarkPromptCase) {
	b.Helper()
	setRouteBenchmarkEnv := routeBenchmarkEnvSetter(b)
	setRouteBenchmarkEnv(home, codexHome)
	preview := ExplainPromptRouteForCWD(cwd, tc.prompt)
	if len(preview.Activations) != tc.activations {
		b.Fatalf("%s expected %d activations, got %#v", tc.name, tc.activations, preview.Activations)
	}
}

func routeBenchmarkEnvSetter(b *testing.B) func(home string, codexHome string) {
	b.Helper()
	oldHome, hadHome := os.LookupEnv("HOME")
	oldCodexHome, hadCodexHome := os.LookupEnv("CODEX_HOME")
	b.Cleanup(func() {
		restoreRouteBenchmarkEnv("HOME", oldHome, hadHome)
		restoreRouteBenchmarkEnv("CODEX_HOME", oldCodexHome, hadCodexHome)
	})
	return func(home string, codexHome string) {
		if err := os.Setenv("HOME", home); err != nil {
			b.Fatalf("set HOME: %v", err)
		}
		if strings.TrimSpace(codexHome) == "" {
			if err := os.Unsetenv("CODEX_HOME"); err != nil {
				b.Fatalf("unset CODEX_HOME: %v", err)
			}
			return
		}
		if err := os.Setenv("CODEX_HOME", codexHome); err != nil {
			b.Fatalf("set CODEX_HOME: %v", err)
		}
	}
}

func restoreRouteBenchmarkEnv(key string, value string, hadValue bool) {
	if hadValue {
		_ = os.Setenv(key, value)
		return
	}
	_ = os.Unsetenv(key)
}

func writeRouteRuntimeDocBenchmarkFile(b *testing.B, codexHome string, skill string) string {
	b.Helper()
	path := filepath.Join(codexHome, "skills", skill, "RUNTIME.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		b.Fatalf("mkdir runtime doc: %v", err)
	}
	if err := os.WriteFile(path, routeRuntimeDocBenchmarkContent(skill), 0o644); err != nil {
		b.Fatalf("write runtime doc: %v", err)
	}
	return path
}

func routeRuntimeDocBenchmarkContent(skill string) []byte {
	return []byte(fmt.Sprintf("# %s runtime\n\n", skill) + strings.Repeat("- Keep lazy skill activation fast, deterministic, and scoped to the triggered runtime document.\n", 96))
}

func readFirstRouteRuntimeDoc(preview routePreview) ([]byte, error) {
	for _, activation := range preview.Activations {
		if activation.DocLabel == routeDocLabelRuntime || activation.DocLabel == "" {
			return os.ReadFile(activation.RuntimePath)
		}
	}
	return nil, fmt.Errorf("route preview did not activate a runtime document")
}
