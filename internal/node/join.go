package node

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/buildinfo"
	"github.com/xraph/loomplane/pkg/client"
	"github.com/xraph/loomplane/pkg/nodeproto"
)

// JoinOptions is what Join needs to join this machine to a Loomplane.
type JoinOptions struct {
	// URL is the Loomplane to join, such as https://loomplane.example.com.
	URL string
	// Token is the single-use join token from Loomplane.
	Token  string
	Name   string
	Labels map[string]string
	// Endpoints is what this machine allows Loomplane to use. Nil means ask
	// with Prompt; with no Prompt either, nothing is allowed.
	Endpoints []nodeproto.Endpoint
	State     State
	// Prompt chooses the endpoints from what Detect found. It runs only when
	// Endpoints is nil.
	Prompt func(nodeproto.Inventory) ([]nodeproto.Endpoint, error)
	// Out receives the preflight table. Nil means os.Stdout.
	Out io.Writer
	// Detect tunes machine detection; the zero value inspects this machine.
	Detect DetectOptions
}

// Join detects this machine, checks its clock against Loomplane's, joins with
// the token and saves node.json. The clock is checked first, so a machine
// that cannot join does not spend its token.
func Join(ctx context.Context, o JoinOptions) (*Config, error) {
	out := o.Out
	if out == nil {
		out = os.Stdout
	}
	base, err := nodeproto.NormalizeAudience(o.URL)
	if err != nil {
		return nil, fmt.Errorf("%q is not a Loomplane URL: it must be an http or https address", o.URL)
	}
	switch existing, lerr := o.State.LoadConfig(); {
	case lerr == nil:
		return nil, fmt.Errorf("this machine already joined %s as %s; run loomctl node leave first, or use another --state-dir", existing.URL, existing.NodeID)
	case !errors.Is(lerr, fs.ErrNotExist):
		return nil, lerr
	}

	inv := Detect(ctx, o.Detect)
	skew, known, skewErr := serverClockSkew(ctx, base)
	printPreflight(out, inv, skew, known)
	if skewErr != nil {
		return nil, skewErr
	}

	eps := o.Endpoints
	if eps == nil && o.Prompt != nil {
		if eps, err = o.Prompt(inv); err != nil {
			return nil, err
		}
	}
	if eps == nil {
		eps = []nodeproto.Endpoint{}
	}
	inv.Endpoints = slices.Clone(eps)

	// There is no node.json, so any key here belonged to a node that left,
	// was revoked or never finished joining. Loomplane refuses a key it has
	// seen, so start a fresh one.
	if rerr := o.State.RetireIdentity(time.Now()); rerr != nil {
		return nil, rerr
	}
	priv, err := o.State.Identity()
	if err != nil {
		return nil, err
	}
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("the identity key is not an ed25519 key")
	}
	res, err := client.NodeJoin(ctx, base, client.JoinRequest{
		Token: o.Token, Name: o.Name, Labels: o.Labels, Endpoints: eps, Inventory: inv,
		PublicKey:   base64.StdEncoding.EncodeToString(pub),
		Signature:   base64.StdEncoding.EncodeToString(nodeproto.SignJoin(priv, o.Token, o.Name)),
		NodeVersion: buildinfo.Version, ProtocolVersion: nodeproto.Version,
	})
	if err != nil {
		return nil, err
	}
	granted := res.Endpoints
	if granted == nil {
		granted = []nodeproto.Endpoint{}
	}
	cfg := Config{URL: base, NodeID: res.NodeID, OrgID: res.OrgID, IdentityGeneration: res.IdentityGeneration,
		Endpoints: granted, IngressMode: nodeproto.IngressNone}
	if inv.Docker != nil {
		// Detected, whether or not it was allowed: the service user needs
		// the socket's group, and the runtime looks for Docker here first.
		cfg.DockerSocket = inv.Docker.Socket
	}
	// The ceiling first: a node.json with no allow.json beside it serves
	// nothing, which is the safe way round if the second write fails.
	if err = o.State.SaveCeiling(Ceiling{NodeID: res.NodeID, Endpoints: granted}, -1); err != nil {
		return nil, fmt.Errorf("joined as node %s, but saving its allowlist in %s failed: %w; revoke that node in Loomplane and join again", res.NodeID, o.State.Dir, err)
	}
	if err = o.State.SaveConfig(cfg); err != nil {
		return nil, fmt.Errorf("joined as node %s, but saving %s failed: %w; revoke that node in Loomplane and join again", res.NodeID, o.State.Dir, err)
	}
	_, _ = fmt.Fprintf(out, "\nJoined %s as node %s.\n", base, res.NodeID)
	return &cfg, nil
}

// serverClockSkew asks Loomplane for a challenge and compares the Date header
// with this machine's clock, at the middle of the round trip. known is false
// when the server sent no usable Date. A skew past nodeproto.MaxSkew is an
// error, since the node's signed challenges would be refused anyway.
func serverClockSkew(ctx context.Context, base string) (skew time.Duration, known bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/node/v1/challenge?node=preflight", http.NoBody)
	if err != nil {
		return 0, false, err
	}
	sent := time.Now()
	res, err := (&http.Client{}).Do(req)
	if err != nil {
		return 0, false, fmt.Errorf("cannot reach Loomplane at %s: %w", base, err)
	}
	defer func() { _ = res.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10)) //nolint:errcheck // only the Date header matters
	mid := sent.Add(time.Since(sent) / 2)
	date, perr := http.ParseTime(res.Header.Get("Date"))
	if perr != nil {
		return 0, false, nil
	}
	skew = date.Sub(mid)
	if abs := max(skew, -skew); abs > nodeproto.MaxSkew {
		return skew, true, fmt.Errorf("this machine's clock is off by %s; fix NTP and join again", abs.Round(time.Second))
	}
	return skew, true, nil
}

func printPreflight(w io.Writer, inv nodeproto.Inventory, skew time.Duration, known bool) {
	line := func(label, format string, a ...any) {
		_, _ = fmt.Fprintf(w, "  %-13s %s\n", label, fmt.Sprintf(format, a...))
	}
	_, _ = fmt.Fprintln(w, "This machine")
	var rts []string
	if inv.Docker != nil {
		rts = append(rts, fmt.Sprintf("docker %s at %s", inv.Docker.Version, inv.Docker.Socket))
	}
	if inv.Podman != nil {
		rts = append(rts, fmt.Sprintf("podman %s at %s", inv.Podman.Version, inv.Podman.Socket))
	}
	if len(rts) == 0 {
		rts = []string{"none found"}
	}
	line("runtime", "%s", strings.Join(rts, ", "))
	gpus := "none"
	if len(inv.GPUs) > 0 {
		names := make([]string, len(inv.GPUs))
		for i, g := range inv.GPUs {
			names[i] = fmt.Sprintf("%s %s (%d MiB)", g.Vendor, g.Model, g.MemoryMiB)
		}
		gpus = strings.Join(names, ", ")
	}
	line("gpus", "%s", gpus)
	line("arch", "%s", inv.Arch)
	line("cpu", "%d cores", inv.CPUs)
	line("memory", "%d MiB", inv.MemoryMiB)
	line("disk", "%d GiB", inv.DiskGiB)
	ports := inv.Ports80443
	if ports == "" {
		ports = "unknown"
	}
	line("ports 80/443", "%s", ports)
	if known {
		line("clock", "%s from Loomplane's", max(skew, -skew).Round(time.Second))
	} else {
		line("clock", "not checked: Loomplane sent no Date")
	}
}
