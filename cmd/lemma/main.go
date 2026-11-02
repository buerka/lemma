// Command lemma is the full alias for lem.
package main

import (
	"os"

	"github.com/buerka/lemma/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
