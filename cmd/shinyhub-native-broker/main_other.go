//go:build !linux

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "native user isolation requires Linux; no fallback")
	os.Exit(125)
}
