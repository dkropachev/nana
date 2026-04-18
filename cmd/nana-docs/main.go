package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/Yeachan-Heo/nana/internal/gocli"
)

func main() {
	outputPath := filepath.Join("docs", "command-reference.html")
	if len(os.Args) > 1 {
		outputPath = os.Args[1]
	}
	if err := os.WriteFile(outputPath, []byte(gocli.RenderCommandReferenceHTML()), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "nana-docs: %v\n", err)
		os.Exit(1)
	}
}
