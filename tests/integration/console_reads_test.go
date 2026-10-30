//go:build integration

package integration

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/xraph/forge"
	"github.com/xraph/forge/extensions/dashboard/contract"

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/config"
)

// consoleReadsFile lists every read registered with the console, one
// "contributor intent@version kind capability open|blocked" per line, where
// open means the console gate lets a platform admin send it.
//
// The gate opens every registered read it doesn't block, so a library
// release that adds or changes one opens it without anyone looking. This
// test fails until the list matches. Before you add a line, or turn one to
// open, read the intent's handler and its response type, and block it in
// extensions/identity/console_allowlist.go (consoleBlockedQueries) instead
// if its response can carry a secret value, a credential, a token, a raw key
// or hash, or a presigned or signed URL that downloads content.
//
// Two things open data without this list changing, so each needs its own
// review. A library release can change what an intent returns while keeping
// its name and version, so read the response types on every library bump,
// not only the new lines here. And Dispatch's reads all answer 503 today
// because its operator boundary isn't wired: wiring it opens every Dispatch
// read on this list at once.
const consoleReadsFile = "testdata/console_reads.txt"

func TestConsoleReadsReviewed(t *testing.T) {
	cfg := config.Defaults()
	r, _ := freshSetup(t, &cfg)
	defer r.stop()
	app, ok := r.app.(forge.App)
	if !ok {
		t.Fatalf("the running app is a %T, not a forge.App", r.app)
	}
	ext, err := app.GetExtension(identity.Name)
	if err != nil {
		t.Fatal(err)
	}
	id, ok := ext.(*identity.Extension)
	if !ok {
		t.Fatalf("%s is a %T", identity.Name, ext)
	}
	reg := id.ConsoleRegistry()
	if reg == nil {
		t.Fatal("the console has no contract registry")
	}
	var got []string
	for _, m := range reg.All() {
		for _, in := range m.Intents {
			if in.Kind != contract.IntentKindQuery && in.Capability != contract.CapRead {
				continue
			}
			gate := "blocked"
			if id.ConsoleOpens(m.Contributor.Name, in) {
				gate = "open"
			}
			got = append(got, fmt.Sprintf("%s %s@%d %s %s %s", m.Contributor.Name, in.Name, in.Version, in.Kind, in.Capability, gate))
		}
	}
	slices.Sort(got)
	raw, err := os.ReadFile(consoleReadsFile)
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, line := range strings.Split(string(raw), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			want = append(want, line)
		}
	}
	slices.Sort(want)
	var added, gone []string
	for _, line := range got {
		if !slices.Contains(want, line) {
			added = append(added, line)
		}
	}
	for _, line := range want {
		if !slices.Contains(got, line) {
			gone = append(gone, line)
		}
	}
	if len(added) > 0 || len(gone) > 0 {
		t.Errorf("the console's reads changed; review each new one (see consoleReadsFile) and update %s\nnew or changed:\n  %s\nno longer registered:\n  %s",
			consoleReadsFile, strings.Join(added, "\n  "), strings.Join(gone, "\n  "))
	}
}
