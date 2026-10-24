package main

import (
	"fmt"
	"os"

	"github.com/Warewave-Technology/gaia/internal/cli"
	"github.com/Warewave-Technology/gaia/internal/version"
)

func main() {
	if err := cli.NewIPAMCommand(version.Version).Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
