// Command check-downloads validates the release tree before packaging or deploy.
package main

import (
	"fmt"
	"os"

	"github.com/ehrlich-b/wingthing/internal/downloads"
)

func main() {
	root := "downloads"
	if len(os.Args) == 2 {
		root = os.Args[1]
	}
	if len(os.Args) > 2 {
		fmt.Fprintln(os.Stderr, "Usage: check-downloads [downloads-directory]")
		os.Exit(2)
	}
	if err := downloads.CheckStaged(root); err != nil {
		fmt.Fprintf(os.Stderr, "Deployment refused: %s; stage the verified release tree with cp -R /path/to/verified/downloads/. downloads/ as described in docs/download-mirror.md.\n", err)
		os.Exit(1)
	}
}
