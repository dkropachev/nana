package version

import (
	"fmt"
	"io"
	"runtime"
)

// Version is injected at build time by Go-native build helpers.
var Version = "dev"

func Print(w io.Writer) {
	fmt.Fprintf(w, "nana v%s\n", Version)
	fmt.Fprintf(w, "Go %s\n", runtime.Version())
	fmt.Fprintf(w, "Platform: %s %s\n", runtime.GOOS, runtime.GOARCH)
}
