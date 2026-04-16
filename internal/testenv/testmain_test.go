package testenv

import (
	"fmt"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	cleanup, err := Activate("internal/testenv")
	if err != nil {
		fmt.Fprintf(os.Stderr, "activate isolated test env: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	cleanup()
	os.Exit(code)
}
