package gocli

import (
	"fmt"
	"os"
	"testing"

	"github.com/dkropachev/nana/internal/testenv"
)

func TestMain(m *testing.M) {
	cleanup, err := testenv.Activate("internal/gocli")
	if err != nil {
		fmt.Fprintf(os.Stderr, "activate isolated test env: %v\n", err)
		os.Exit(1)
	}
	githubTokenResolverOverride = func(apiBaseURL string) (string, error) {
		if token := os.Getenv("GH_TOKEN"); token != "" {
			return token, nil
		}
		if token := os.Getenv("GITHUB_TOKEN"); token != "" {
			return token, nil
		}
		return resolveGithubTokenForAPIBaseDefault(apiBaseURL)
	}
	code := m.Run()
	githubTokenResolverOverride = nil
	cleanup()
	os.Exit(code)
}
