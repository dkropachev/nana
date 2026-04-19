package main

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	_ = os.Setenv("NANA_ALLOW_ENV_GITHUB_TOKEN", "1")
	os.Exit(m.Run())
}
