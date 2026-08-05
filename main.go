package main

import (
	"os"

	"ezterm/internal/cli"
)

// main is the entry point for the ezterm binary (CLI + daemon dispatch).
func main() {
	os.Exit(cli.Run(os.Args[1:]))
}
