package ops

import (
	"slices"
	"testing"

	"github.com/xraph/forge/cli"
)

func TestNodeCommandIsRegistered(t *testing.T) {
	var found cli.Command
	for _, c := range Commands(nil) {
		if c.Name() == "node" {
			found = c
		}
	}
	if found == nil {
		t.Fatal("loomplane has no node command")
	}
	subs := make([]string, 0, len(found.Subcommands()))
	for _, s := range found.Subcommands() {
		subs = append(subs, s.Name())
	}
	for _, want := range []string{"join", "run", "status", "leave", "allow", "list", "revoke"} {
		if !slices.Contains(subs, want) {
			t.Errorf("node %s is missing; have %v", want, subs)
		}
	}
}

// A machine that joins as a node has no database and no configuration, so its
// node commands must not load Loomplane's settings.
func TestLoomplaneNodeNeedsNoApp(t *testing.T) {
	settings := func() (Settings, error) { panic("node commands must not load settings") }
	c := cli.New(cli.Config{Name: "loomplane"})
	for _, cmd := range Commands(settings) {
		if err := c.AddCommand(cmd); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Run([]string{"loomplane", "node", "status", "--state-dir", t.TempDir() + "/state"}); err != nil {
		t.Fatalf("node status: %v", err)
	}
}
