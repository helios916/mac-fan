//go:build !darwin

package main

import (
	"fmt"
	"os"
	"runtime"
)

// mac-fan talks to Apple's SMC through IOKit, so it cannot do anything useful
// on other operating systems. Building for them still succeeds and prints a
// clear message instead of failing at link time.
func main() {
	fmt.Fprintf(os.Stderr, "mac-fan only supports Apple Silicon Macs running macOS (detected %s/%s).\n", runtime.GOOS, runtime.GOARCH)
	fmt.Fprintln(os.Stderr, "Fan control uses Apple's System Management Controller via IOKit, which is macOS-only.")
	os.Exit(1)
}
