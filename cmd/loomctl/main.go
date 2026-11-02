// Command loomctl is Loomplane's command-line client: plan, approve and
// follow deployments from your terminal or CI, with an API key.
//
//	loomctl login --url https://loomplane.example.com --key lp_live_...
//	loomctl plan mono --env production
//	loomctl approve <plan-id>
//	loomctl node join --token lpj_...   join this machine to Loomplane as a node
//	loomctl node --help                 join, run, status, allow, leave, list, revoke
//
// In CI, set LOOMPLANE_URL and LOOMPLANE_API_KEY instead of logging in.
package main

import (
	"fmt"
	"os"

	"github.com/xraph/loomplane/internal/buildinfo"
	"github.com/xraph/loomplane/internal/ctl"
	"github.com/xraph/loomplane/internal/node"
)

func main() {
	if err := ctl.New(ctl.Options{Version: buildinfo.Version}).Run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "loomctl:", err)
		// 3 is a node that Loomplane revoked; everything else is 1.
		os.Exit(node.ExitCode(err))
	}
}
