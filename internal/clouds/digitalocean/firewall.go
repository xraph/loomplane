package digitalocean

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/xraph/loomplane/pkg/provider"
)

var _ provider.ServerFirewallAdapter = (*Client)(nil)

// firewallTagPattern is the only tag shape a management firewall uses, and
// so the only one CreateServer accepts as a FirewallTag.
var firewallTagPattern = regexp.MustCompile(`^loomplane-ssh-[1-9]\d{0,4}$`)

// maxFirewallSources is DigitalOcean's limit on one rule's addresses.
const maxFirewallSources = 1000

var everywhere = []string{"0.0.0.0/0", "::/0"}

type firewallTargets struct {
	Addresses        []string `json:"addresses,omitempty"`
	Tags             []string `json:"tags,omitempty"`
	DropletIDs       []int64  `json:"droplet_ids,omitempty"`
	LoadBalancerUIDs []string `json:"load_balancer_uids,omitempty"`
	KubernetesIDs    []string `json:"kubernetes_ids,omitempty"`
}

type firewallRule struct {
	Action       string           `json:"action,omitempty"`
	Protocol     string           `json:"protocol"`
	Ports        string           `json:"ports"`
	Sources      *firewallTargets `json:"sources,omitempty"`
	Destinations *firewallTargets `json:"destinations,omitempty"`
}

type firewall struct {
	ID            string         `json:"id,omitempty"`
	Name          string         `json:"name"`
	InboundRules  []firewallRule `json:"inbound_rules"`
	OutboundRules []firewallRule `json:"outbound_rules"`
	Tags          []string       `json:"tags"`
	DropletIDs    []int64        `json:"droplet_ids"`
}

// managementRules lets sources reach port over TCP, and everyone reach every
// other port, as they could with no firewall. A DigitalOcean firewall drops
// what no rule allows, in and out, so everything else is allowed explicitly.
// Allow rules only: a deny rule would win over Loomplane's own allow.
func managementRules(port int, sources []string) (inbound, outbound []firewallRule) {
	from := func(addresses []string) *firewallTargets { return &firewallTargets{Addresses: addresses} }
	inbound = append(inbound, firewallRule{Protocol: "tcp", Ports: strconv.Itoa(port), Sources: from(sources)})
	span := func(lo, hi int) string {
		if lo == hi {
			return strconv.Itoa(lo)
		}
		return strconv.Itoa(lo) + "-" + strconv.Itoa(hi)
	}
	if port > 1 {
		inbound = append(inbound, firewallRule{Protocol: "tcp", Ports: span(1, port-1), Sources: from(everywhere)})
	}
	if port < 65535 {
		inbound = append(inbound, firewallRule{Protocol: "tcp", Ports: span(port+1, 65535), Sources: from(everywhere)})
	}
	for _, protocol := range []string{"udp", "icmp"} {
		inbound = append(inbound, firewallRule{Protocol: protocol, Ports: "0", Sources: from(everywhere)})
	}
	for _, protocol := range []string{"tcp", "udp", "icmp"} {
		outbound = append(outbound, firewallRule{Protocol: protocol, Ports: "0", Destinations: &firewallTargets{Addresses: everywhere}})
	}
	return inbound, outbound
}

// sameRules compares rules as DigitalOcean reads them back: allow is the
// default action, ICMP has no ports, and address order doesn't matter.
func sameRules(a, b []firewallRule) bool {
	norm := func(rules []firewallRule) []string {
		out := make([]string, 0, len(rules))
		for _, r := range rules {
			action, ports := r.Action, r.Ports
			if action == "" {
				action = "allow"
			}
			if r.Protocol == "icmp" || ports == "all" {
				ports = "0"
			}
			targets := r.Sources
			if targets == nil {
				targets = r.Destinations
			}
			var t firewallTargets
			if targets != nil {
				t = *targets
			}
			addresses := slices.Clone(t.Addresses)
			slices.Sort(addresses)
			others := len(t.Tags) + len(t.DropletIDs) + len(t.LoadBalancerUIDs) + len(t.KubernetesIDs)
			out = append(out, action+" "+r.Protocol+" "+ports+" "+strconv.Itoa(others)+" "+strings.Join(addresses, ","))
		}
		slices.Sort(out)
		return out
	}
	return slices.Equal(norm(a), norm(b))
}

// managementSources checks and canonicalizes CIDR prefixes. A bare address
// is a prefix of one address.
func managementSources(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	for _, s := range in {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			a, aerr := netip.ParseAddr(s)
			if aerr != nil {
				return nil, provider.ErrInfrastructureInvalid
			}
			p = netip.PrefixFrom(a, a.BitLen())
		}
		out = append(out, p.Masked().String())
	}
	slices.Sort(out)
	out = slices.Compact(out)
	if len(out) == 0 || len(out) > maxFirewallSources {
		return nil, provider.ErrInfrastructureInvalid
	}
	return out, nil
}

// EnsureManagementFirewall keeps one firewall per SSH port, named and tagged
// after it. A missing one is created; an existing one keeps its Droplets and
// tags, and gets Loomplane's rules back if they changed.
func (c *Client) EnsureManagementFirewall(ctx context.Context, credential provider.InfrastructureCredential, f provider.ManagementFirewall) error {
	if f.SSHPort < 1 || f.SSHPort > 65535 {
		return provider.ErrInfrastructureInvalid
	}
	sources, err := managementSources(f.Sources)
	if err != nil {
		return err
	}
	if err := c.infrastructureIdentity(ctx, credential); err != nil {
		return err
	}
	tag := provider.ManagementFirewallTag(f.SSHPort)
	run := func(ctx context.Context) error {
		return c.ensureManagementFirewall(ctx, credential, tag, f.SSHPort, sources)
	}
	if c.Lock == nil {
		return run(ctx)
	}
	// Two creates at once would otherwise both find no firewall and make two.
	return c.Lock(ctx, "digitalocean-firewall/"+credential.AccountID+"/"+tag, run)
}

func (c *Client) ensureManagementFirewall(ctx context.Context, credential provider.InfrastructureCredential, tag string, port int, sources []string) error {
	if err := c.ensureTag(ctx, credential, tag); err != nil {
		return err
	}
	inbound, outbound := managementRules(port, sources)
	existing, err := c.firewallNamed(ctx, credential, tag)
	if err != nil {
		return err
	}
	if existing == nil {
		in := firewall{Name: tag, InboundRules: inbound, OutboundRules: outbound, Tags: []string{tag}, DropletIDs: []int64{}}
		return infrastructureError(c.do(ctx, credential.Token, http.MethodPost, "/v2/firewalls", nil, in, nil), "firewall:create")
	}
	if slices.Contains(existing.Tags, tag) && sameRules(existing.InboundRules, inbound) && sameRules(existing.OutboundRules, outbound) {
		return nil
	}
	// PUT replaces the whole firewall, so it carries the Droplets and tags
	// it already has.
	tags := slices.Clone(existing.Tags)
	if !slices.Contains(tags, tag) {
		tags = append(tags, tag)
	}
	droplets := existing.DropletIDs
	if droplets == nil {
		droplets = []int64{}
	}
	in := firewall{Name: tag, InboundRules: inbound, OutboundRules: outbound, Tags: tags, DropletIDs: droplets}
	return infrastructureError(c.do(ctx, credential.Token, http.MethodPut, "/v2/firewalls/"+url.PathEscape(existing.ID), nil, in, nil), "firewall:update")
}

// ensureTag creates tag unless it exists, so the firewall can name it before
// any Droplet carries it.
func (c *Client) ensureTag(ctx context.Context, credential provider.InfrastructureCredential, tag string) error {
	err := infrastructureError(c.do(ctx, credential.Token, http.MethodGet, "/v2/tags/"+url.PathEscape(tag), nil, nil, nil), "tag:read")
	if !errors.Is(err, provider.ErrInfrastructureNotFound) {
		return err
	}
	in := struct {
		Name string `json:"name"`
	}{tag}
	return infrastructureError(c.do(ctx, credential.Token, http.MethodPost, "/v2/tags", nil, in, nil), "tag:create")
}

// firewallNamed finds the one firewall called name. Two would mean someone
// else made one, and Loomplane won't guess which is its own.
func (c *Client) firewallNamed(ctx context.Context, credential provider.InfrastructureCredential, name string) (*firewall, error) {
	var found *firewall
	seen := map[string]bool{}
	for page := 1; page <= infrastructureMaxPages; page++ {
		var out struct {
			Firewalls []firewall          `json:"firewalls"`
			Links     infrastructureLinks `json:"links"`
		}
		q := url.Values{"page": {strconv.Itoa(page)}, "per_page": {strconv.Itoa(pageSize)}}
		if err := c.do(ctx, credential.Token, http.MethodGet, "/v2/firewalls", q, nil, &out); err != nil {
			return nil, infrastructureError(err, "firewall:read")
		}
		for i := range out.Firewalls {
			fw := out.Firewalls[i]
			if fw.ID == "" || seen[fw.ID] {
				return nil, provider.ErrInfrastructurePagination
			}
			seen[fw.ID] = true
			if fw.Name != name {
				continue
			}
			if found != nil {
				return nil, provider.ErrInfrastructureDrift
			}
			found = &fw
		}
		if out.Links.Pages.Next == "" {
			return found, nil
		}
	}
	return nil, provider.ErrInfrastructurePagination
}
