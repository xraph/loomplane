// Command loomctl is Loomplane's command-line client: plan, approve and
// follow deployments from your terminal or CI, with an API key.
//
//	loomctl login --url https://loomplane.example.com --key lp_live_...
//	loomctl plan mono --env production
//	loomctl approve <plan-id>
//
// In CI, set LOOMPLANE_URL and LOOMPLANE_API_KEY instead of logging in.
package main

import (
	"fmt"
	"os"

	"github.com/xraph/loomplane/internal/buildinfo"
	"github.com/xraph/loomplane/internal/ctl"
)

func main() {
	if err := ctl.New(ctl.Options{Version: buildinfo.Version}).Run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "loomctl:", err)
		os.Exit(1)
	}
}
