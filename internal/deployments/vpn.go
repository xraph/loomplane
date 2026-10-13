package deployments

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/clouds/aws"
	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/clouds/progress"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
)

// VPN makes links between clouds: an HA VPN from an environment's Google
// Cloud project to its AWS account. Google's side is an HA VPN gateway
// and a Cloud Router on the default network; AWS's a virtual private
// gateway on the default VPC, with a customer gateway and a VPN connection
// for each of Google's two endpoints. That's four IPsec tunnels, each
// carrying a BGP session, so either side losing an endpoint keeps the
// link up. The default network (10.128.0.0/9) and VPC (172.31.0.0/16)
// don't overlap, so their routes need no translating.
//
// The link's binding is on its Google Cloud connection. Its ExternalID
// names the AWS connection too, so removing it and drift checks reach
// both.
type VPN struct {
	Google      *gcp.Client
	AWS         *aws.Client
	Credentials Credentials
	// TunnelWait is how long to wait for every tunnel to come up.
	TunnelWait time.Duration
}

var _ Executor = (*VPN)(nil)

// BGP ASNs, private ones: Google's router and AWS's gateway each need
// their own.
const (
	vpnGoogleASN = 64514
	vpnAWSASN    = 64512
	vpnRevision  = "private-link"
	vpnTunnels   = 4
)

// vpnNames are what a link's pieces are called on both clouds.
type vpnNames struct {
	base, gateway, router, peer string
	tunnels                     []string
	customers, connections      [2]string
}

func linkNames(base string) vpnNames {
	n := vpnNames{base: base, gateway: base + "-gw", router: base + "-router", peer: base + "-peer"}
	for i := range vpnTunnels {
		n.tunnels = append(n.tunnels, base+"-t"+strconv.Itoa(i))
	}
	for i := range 2 {
		n.customers[i] = base + "-cgw" + strconv.Itoa(i)
		n.connections[i] = base + "-vpn" + strconv.Itoa(i)
	}
	return n
}

// insideBlock is the link-local /24 a link's tunnels take /30s from. It
// differs between environments, and keeps clear of the blocks AWS
// reserves at the start of 169.254.0.0/16.
func insideBlock(base string) int {
	sum := sha256.Sum256([]byte(base))
	return 10 + int(sum[0])%240
}

// linkRef is how a binding names a link: the AWS connection, and the base
// name its pieces share.
func linkRef(external string) (awsConn, base string, err error) {
	awsConn, base, ok := strings.Cut(external, "/")
	if !ok || awsConn == "" || base == "" {
		return "", "", fmt.Errorf("the binding names no link: %q", external)
	}
	return awsConn, base, nil
}

func (v *VPN) google(ctx context.Context, cred []byte) (*gcp.Session, error) {
	key, err := gcp.ParseKey(cred)
	if err != nil {
		return nil, err
	}
	return v.Google.Session(ctx, key)
}

func (v *VPN) amazon(ctx context.Context, orgID, connID string) (*aws.Session, error) {
	conn, cred, err := v.Credentials.Credential(ctx, orgID, connID)
	if err != nil {
		return nil, fmt.Errorf("the link's AWS connection: %w", err)
	}
	key, err := aws.ParseKey(cred)
	if err != nil {
		return nil, err
	}
	return v.AWS.Session(key, conn.Details["region"], conn.Account), nil
}

// secrets are the link's four pre-shared keys: the ones kept for it, or
// new ones, kept before either cloud sees them so a retry uses the same.
func secrets(ctx context.Context, job *Job) ([]string, error) {
	kept, err := job.Recall(ctx, passwordField)
	if err == nil && len(kept) > 0 {
		var out []string
		if json.Unmarshal(kept, &out) == nil && len(out) == vpnTunnels {
			return out, nil
		}
	}
	if err != nil && !errors.Is(err, ErrNoCredential) {
		return nil, err
	}
	out := make([]string, vpnTunnels)
	for i := range out {
		// AWS takes letters, digits, periods and underscores, and not a
		// leading zero.
		out[i] = "k" + rand.Text()
	}
	data, _ := json.Marshal(out) //nolint:errcheck // strings always encode
	if err := job.Keep(ctx, passwordField, data); err != nil {
		return nil, err
	}
	return out, nil
}

// Apply implements Executor.
func (v *VPN) Apply(ctx context.Context, job *Job) (*Outcome, error) {
	cfg := job.Action.Resource
	if cfg == nil || cfg.Kind != planning.LinkKind {
		return nil, fmt.Errorf("%w: the cross-cloud vpn makes links, not %s", ErrNoExecutor, job.Action.Subject)
	}
	awsConn := ""
	if job.Plan != nil {
		awsConn = job.Plan.Connections["aws"]
	}
	if awsConn == "" {
		return nil, errors.New("the plan has no AWS connection for the link's other side")
	}
	g, err := v.google(ctx, job.Credential)
	if err != nil {
		return nil, err
	}
	a, err := v.amazon(ctx, job.Deployment.OrgID, awsConn)
	if err != nil {
		return nil, err
	}
	ctx = progress.With(ctx, func(m string) { job.Progress(StepDeploying, m) })
	names := linkNames(shortName("lp-" + job.Action.Name + "-" + job.Deployment.Environment + "-" + idSuffix(job.Deployment.ProjectID)))
	keys, err := secrets(ctx, job)
	if err != nil {
		return nil, err
	}
	region := cfg.Region

	job.Progress(StepDeploying, "Making the Google Cloud side of the link")
	ips, err := g.EnsureVPNGateway(ctx, region, names.gateway)
	if err != nil {
		return nil, err
	}
	if rerr := g.EnsureRouter(ctx, region, names.router, vpnGoogleASN); rerr != nil {
		return nil, rerr
	}

	job.Progress(StepDeploying, "Making the AWS side of the link")
	net, err := a.DefaultNetwork(ctx)
	if err != nil {
		return nil, err
	}
	vgw, err := a.EnsureVPNGateway(ctx, names.base, vpnAWSASN, net.VPC)
	if err != nil {
		return nil, err
	}
	block := insideBlock(names.base)
	var outside []string
	var peers []gcp.BGPPeer
	for i := range 2 {
		cgw, cerr := a.EnsureCustomerGateway(ctx, names.customers[i], ips[i], vpnGoogleASN)
		if cerr != nil {
			return nil, cerr
		}
		specs := [2]aws.TunnelSpec{}
		for j := range 2 {
			t := i*2 + j
			specs[j] = aws.TunnelSpec{InsideCIDR: fmt.Sprintf("169.254.%d.%d/30", block, t*4), Secret: keys[t]}
		}
		tunnels, terr := a.EnsureVPNConnection(ctx, names.connections[i], cgw, vgw, specs)
		if terr != nil {
			return nil, terr
		}
		for j, t := range tunnels {
			n := i*2 + j
			outside = append(outside, t.Outside)
			// AWS takes the first address of each /30, Google the second.
			peers = append(peers, gcp.BGPPeer{
				Name: names.tunnels[n], Tunnel: names.tunnels[n], PeerASN: vpnAWSASN,
				IPRange: fmt.Sprintf("169.254.%d.%d/30", block, n*4+2), PeerIP: fmt.Sprintf("169.254.%d.%d", block, n*4+1),
			})
		}
	}

	job.Progress(StepDeploying, "Joining the two sides")
	if perr := g.EnsurePeerGateway(ctx, names.peer, outside); perr != nil {
		return nil, perr
	}
	for n, name := range names.tunnels {
		t := gcp.Tunnel{
			Name: name, Gateway: names.gateway, Interface: n / 2, Peer: names.peer, PeerIndex: n,
			Secret: keys[n], Router: names.router,
		}
		if terr := g.EnsureTunnel(ctx, region, t); terr != nil {
			return nil, terr
		}
	}
	if perr := g.SetPeers(ctx, region, names.router, peers); perr != nil {
		return nil, perr
	}

	// What a service on Google Cloud reaches over the link: private
	// services behind the internal load balancer, and the databases and
	// caches. The groups are the ones ECS, RDS and ElastiCache use, made
	// here if the link comes first.
	env := awsEnvironment(job)
	for _, g := range []struct{ name, what string }{
		{"loomplane-" + env + "-ilb", "Loomplane internal load balancer for " + env},
		{"loomplane-" + env + "-db", "Loomplane databases for " + env},
		{"loomplane-" + env + "-cache", "Loomplane caches for " + env},
	} {
		group, gerr := a.EnsureSecurityGroup(ctx, net.VPC, g.name, g.what)
		if gerr != nil {
			return nil, gerr
		}
		if aerr := a.AllowFromCIDR(ctx, group, LinkedRange); aerr != nil {
			return nil, aerr
		}
	}

	if werr := v.waitTunnels(ctx, g, region, names.tunnels); werr != nil {
		return nil, werr
	}
	return &Outcome{ExternalID: awsConn + "/" + names.base, Revision: vpnRevision}, nil
}

// waitTunnels waits until every tunnel is up.
func (v *VPN) waitTunnels(ctx context.Context, g *gcp.Session, region string, tunnels []string) error {
	limit := v.TunnelWait
	if limit == 0 {
		limit = 15 * time.Minute
	}
	deadline := time.Now().Add(limit)
	for {
		up, status, err := tunnelsUp(ctx, g, region, tunnels)
		if err != nil {
			return err
		}
		if up == len(tunnels) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("only %d of the link's %d tunnels came up in %s; the last one says %s. Check the AWS VPN connections' tunnel details", up, len(tunnels), limit, status)
		}
		progress.Report(ctx, fmt.Sprintf("Waiting for the tunnels: %d of %d up", up, len(tunnels)))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(v.poll()):
		}
	}
}

func (v *VPN) poll() time.Duration {
	if v.TunnelWait > 0 && v.TunnelWait < time.Minute {
		return 100 * time.Millisecond
	}
	return 15 * time.Second
}

func tunnelsUp(ctx context.Context, g *gcp.Session, region string, tunnels []string) (up int, last string, err error) {
	for _, t := range tunnels {
		status, serr := g.TunnelStatus(ctx, region, t)
		if serr != nil {
			return 0, "", serr
		}
		if status == "ESTABLISHED" {
			up++
		} else {
			last = status
		}
	}
	return up, last, nil
}

// Remove implements Executor: Google's side goes first, so neither cloud
// keeps a tunnel to something gone.
func (v *VPN) Remove(ctx context.Context, job *Job, b *planning.Binding) error {
	awsConn, base, err := linkRef(b.ExternalID)
	if err != nil {
		return err
	}
	g, err := v.google(ctx, job.Credential)
	if err != nil {
		return err
	}
	names := linkNames(base)
	if derr := g.DeleteVPN(ctx, b.Region, names.tunnels, names.router, names.peer, names.gateway); derr != nil {
		return derr
	}
	a, err := v.amazon(ctx, b.OrgID, awsConn)
	if err != nil {
		return err
	}
	net, err := a.DefaultNetwork(ctx)
	if err != nil {
		return err
	}
	if derr := a.DeleteVPN(ctx, names.base, names.connections[:], net.VPC); derr != nil {
		return derr
	}
	return job.Forget(ctx)
}

// Observe implements Executor. A link with a tunnel down has changed: the
// next plan updates it, which brings back whatever's missing.
func (v *VPN) Observe(ctx context.Context, _ *connections.Connection, cred []byte, b *planning.Binding) (*Observation, error) {
	_, base, err := linkRef(b.ExternalID)
	if err != nil {
		return nil, err
	}
	g, err := v.google(ctx, cred)
	if err != nil {
		return nil, err
	}
	names := linkNames(base)
	ok, err := g.VPNGatewayExists(ctx, b.Region, names.gateway)
	if err != nil || !ok {
		return &Observation{}, err
	}
	up, _, err := tunnelsUp(ctx, g, b.Region, names.tunnels)
	if err != nil {
		return nil, err
	}
	o := &Observation{Exists: true, Revision: vpnRevision}
	if up < vpnTunnels {
		o.Revision = fmt.Sprintf("%s, %d of %d tunnels up", vpnRevision, up, vpnTunnels)
	}
	return o, nil
}
