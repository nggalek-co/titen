//go:build !linux

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "The PulseAudio capture helper requires Linux")
	os.Exit(1)
}
