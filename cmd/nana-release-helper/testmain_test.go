package main

import (
	"fmt"
	"os"
	"testing"

	"github.com/dkropachev/nana/internal/testenv"
)

func TestMain(m *testing.M) {
	cleanup, err := testenv.Activate("cmd/nana-release-helper")
	if err != nil {
		fmt.Fprintf(os.Stderr, "activate isolated test env: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	cleanup()
	os.Exit(code)
}
