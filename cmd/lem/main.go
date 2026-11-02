// Command lem is a git-native progress ledger for human–agent research.
package main

import (
	"os"

	"github.com/buerka/lemma/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
