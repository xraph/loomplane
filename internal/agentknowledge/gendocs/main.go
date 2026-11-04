// Command gendocs writes the docs bundle the binary embeds. Run it with
// "make docs-bundle" after editing apps/docs/content.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/xraph/loomplane/internal/agentknowledge"
)

func main() {
	root := flag.String("root", "../../apps/docs/content/docs", "directory holding the MDX docs")
	out := flag.String("out", "docs.json", "file to write")
	flag.Parse()
	if err := run(*root, *out); err != nil {
		fmt.Fprintln(os.Stderr, "gendocs:", err)
		os.Exit(1)
	}
}

func run(root, out string) error {
	b, err := agentknowledge.BuildBundle(os.DirFS(root))
	if err != nil {
		return err
	}
	data, err := agentknowledge.EncodeBundle(b)
	if err != nil {
		return err
	}
	return os.WriteFile(out, data, 0o600)
}
