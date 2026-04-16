package gocli

import (
	"slices"
	"testing"
	"time"
)

func TestInferGithubInitialRepoConsiderationsFromFiles(t *testing.T) {
	files := []string{
		"go.mod",
		"internal/api/openapi.yaml",
		"security/policy.md",
		"tests/example_test.go",
	}

	inference := inferGithubInitialRepoConsiderationsFromFiles(files, "acme/widget-api", githubVerificationPlan{})
	for _, expected := range []string{"api", "dependency", "qa", "security"} {
		if !slices.Contains(inference.Considerations, expected) {
			t.Fatalf("expected consideration %q in %#v", expected, inference.Considerations)
		}
	}
}

func TestInferGithubHotPathProfileFromFilesExtractsSortedUniqueTokens(t *testing.T) {
	now := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	files := []string{
		"services/openapi/user-profile.yaml",
		"pkg/api/user_profile_benchmark.proto",
		"pkg/graphql/query_runner.go",
	}

	profile := inferGithubHotPathProfileFromFiles(files, now)
	if profile == nil {
		t.Fatal("expected hot-path profile")
	}
	if !slices.Equal(profile.APISurfaceFiles, []string{
		"pkg/api/user_profile_benchmark.proto",
		"pkg/graphql/query_runner.go",
		"services/openapi/user-profile.yaml",
	}) {
		t.Fatalf("unexpected api surface files: %#v", profile.APISurfaceFiles)
	}
	if !slices.Equal(profile.HotPathAPIFiles, []string{"pkg/api/user_profile_benchmark.proto"}) {
		t.Fatalf("unexpected hot-path api files: %#v", profile.HotPathAPIFiles)
	}
	if !slices.Equal(profile.APIIdentifierTokens, []string{"benchmark", "profile", "query", "runner", "user"}) {
		t.Fatalf("unexpected api identifier tokens: %#v", profile.APIIdentifierTokens)
	}
	if profile.AnalyzedAt != now.Format(time.RFC3339) {
		t.Fatalf("unexpected analyzed timestamp: %+v", profile)
	}
}
