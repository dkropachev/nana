package gocli

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/dkropachev/nana/internal/testenv"
)

var preservedOptionalTestEnvKeys = []string{
	"GH_TOKEN",
	"GITHUB_TOKEN",
	"GITHUB_API_URL",
	"NANA_LIVE_GITHUB",
	"NANA_LIVE_REPO_LOCAL",
	"NANA_LIVE_REPO_FORK_TARGET",
	"NANA_LIVE_REPO_FORK",
	"NANA_LIVE_REPO_REPO",
	"NANA_LIVE_REPO_DISABLED",
}

var preservedOptionalTestEnv = map[string]string{}

func optionalTestEnv(key string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return preservedOptionalTestEnv[key]
}

func TestMain(m *testing.M) {
	preservedEnv := map[string]string{}
	for _, key := range preservedOptionalTestEnvKeys {
		if value, ok := os.LookupEnv(key); ok {
			preservedEnv[key] = value
		}
	}
	preservedOptionalTestEnv = preservedEnv
	cleanup, err := testenv.Activate("internal/gocli")
	if err != nil {
		fmt.Fprintf(os.Stderr, "activate isolated test env: %v\n", err)
		os.Exit(1)
	}
	githubTokenResolverOverride = func(apiBaseURL string) (string, error) {
		if token := strings.TrimSpace(optionalTestEnv("GH_TOKEN")); token != "" {
			return token, nil
		}
		if token := strings.TrimSpace(optionalTestEnv("GITHUB_TOKEN")); token != "" {
			return token, nil
		}
		return resolveGithubTokenForAPIBaseDefault(apiBaseURL)
	}
	code := m.Run()
	githubTokenResolverOverride = nil
	cleanup()
	os.Exit(code)
}
