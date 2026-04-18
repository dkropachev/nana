package nana_test

import (
	"os"
	"strings"
	"testing"
)

func TestBenchmarkWorkflowDoesNotPersistCheckoutCredentials(t *testing.T) {
	workflow := readBenchmarkWorkflow(t)
	checkoutStep := workflowSection(t, workflow, "- uses: actions/checkout@v4", "- uses: actions/setup-go@v5")

	if strings.Contains(checkoutStep, "persist-credentials: true") {
		t.Fatalf("benchmark checkout must not persist GitHub credentials into git config")
	}

	if !strings.Contains(checkoutStep, "persist-credentials: false") {
		t.Fatalf("benchmark checkout must disable persisted credentials before pull_request benchmark code runs")
	}
}

func TestBenchmarkWorkflowPropagatesMakeBenchmarkFailure(t *testing.T) {
	workflow := readBenchmarkWorkflow(t)
	benchmarkStep := workflowSection(t, workflow, "- name: Run Go benchmarks with benchmem", "- name: Publish benchmark summary")

	for _, required := range []string{
		"set +e",
		"make benchmark 2>&1 | tee benchmark-results/go-benchmem.txt",
		"status=${PIPESTATUS[0]}",
		`echo "status=${status}" >> "$GITHUB_OUTPUT"`,
		`exit "${status}"`,
	} {
		if !strings.Contains(benchmarkStep, required) {
			t.Fatalf("benchmark step must contain %q so make benchmark failures are preserved", required)
		}
	}

	outputIndex := strings.Index(benchmarkStep, `echo "status=${status}" >> "$GITHUB_OUTPUT"`)
	exitIndex := strings.Index(benchmarkStep, `exit "${status}"`)
	if exitIndex < outputIndex {
		t.Fatalf("benchmark step must write status output before exiting with the captured status")
	}
}

func TestBenchmarkWorkflowScopesContinueOnErrorToAdvisoryPRBenchmarks(t *testing.T) {
	workflow := readBenchmarkWorkflow(t)
	benchmarkStep := workflowSection(t, workflow, "- name: Run Go benchmarks with benchmem", "- name: Publish benchmark summary")

	if strings.Contains(workflow, "\n    continue-on-error:") {
		t.Fatalf("job-level continue-on-error hides checkout/setup failures; scope it to the benchmark step")
	}

	want := "continue-on-error: ${{ github.event_name == 'pull_request' }}"
	if !strings.Contains(benchmarkStep, want) {
		t.Fatalf("benchmark step must use %q so PR benchmark failures are advisory without hiding scheduled failures", want)
	}
}

func readBenchmarkWorkflow(t *testing.T) string {
	t.Helper()

	content, err := os.ReadFile(".github/workflows/benchmarks.yml")
	if err != nil {
		t.Fatalf("read benchmarks workflow: %v", err)
	}
	return string(content)
}

func workflowSection(t *testing.T, workflow, startMarker, endMarker string) string {
	t.Helper()

	start := strings.Index(workflow, startMarker)
	if start == -1 {
		t.Fatalf("workflow missing section start marker %q", startMarker)
	}

	end := strings.Index(workflow[start:], endMarker)
	if end == -1 {
		t.Fatalf("workflow missing section end marker %q", endMarker)
	}

	return workflow[start : start+end]
}
