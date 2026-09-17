// Command gitsize explains why a git repository's .git directory is big.
package main

import (
	"os"

	"github.com/Mr-hunt-007/gitsize/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
