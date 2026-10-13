//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/config"
)

// fakeCompute is Compute Engine's VPN pieces, in front of the rest of a
// fake Google Cloud: HA VPN gateways, routers, external gateways and
// tunnels, each made by an operation that's already done.
type fakeCompute struct {
	mu        sync.Mutex
	next      http.Handler
	resources map[string]map[string]any // by collection/name
	deleted   []string
}

const computePrefix = "/compute/v1/projects/acme-prod"

func (f *fakeCompute) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, computePrefix) {
		f.next.ServeHTTP(w, r)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	reply := func(status int, v any) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	done := map[string]any{"name": "op-1", "status": "DONE"}
	path := strings.TrimPrefix(r.URL.Path, computePrefix)
	path = strings.TrimPrefix(strings.TrimPrefix(path, "/regions/us-central1"), "/global")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	var body map[string]any
	data, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(data, &body)
	switch {
	case parts[0] == "operations":
		reply(http.StatusOK, done)
	case len(parts) == 1 && r.Method == http.MethodPost:
		key := parts[0] + "/" + body["name"].(string)
		if _, ok := f.resources[key]; ok {
			reply(http.StatusConflict, map[string]any{"error": map[string]any{"message": "exists"}})
			return
		}
		if parts[0] == "vpnGateways" {
			body["vpnInterfaces"] = []any{map[string]any{"id": 0, "ipAddress": "35.0.0.1"}, map[string]any{"id": 1, "ipAddress": "35.0.0.2"}}
		}
		if parts[0] == "vpnTunnels" {
			body["status"] = "ESTABLISHED"
		}
		f.resources[key] = body
		reply(http.StatusOK, done)
	case len(parts) == 2:
		key := parts[0] + "/" + parts[1]
		res, ok := f.resources[key]
		if !ok {
			reply(http.StatusNotFound, map[string]any{"error": map[string]any{"message": "not found"}})
			return
		}
		switch r.Method {
		case http.MethodGet:
			reply(http.StatusOK, res)
		case http.MethodPatch:
			for k, v := range body {
				res[k] = v
			}
			reply(http.StatusOK, done)
		case http.MethodDelete:
			delete(f.resources, key)
			f.deleted = append(f.deleted, key)
			reply(http.StatusOK, done)
		}
	default:
		reply(http.StatusNotFound, map[string]any{"error": map[string]any{"message": "no route " + path}})
	}
}

// fakeVPN is EC2's VPN actions, added to a fake AWS: a virtual private
// gateway, customer gateways, and VPN connections whose tunnels carry
// what they were made with.
type fakeVPN struct {
	mu          sync.Mutex
	gateways    map[string]string // Name tag → ID
	attached    bool
	propagated  bool
	customers   map[string]string // Name tag → address
	connections map[string]url.Values
}

func installFakeVPN(f *fakeAWS) *fakeVPN {
	v := &fakeVPN{gateways: map[string]string{}, customers: map[string]string{}, connections: map[string]url.Values{}}
	ec2 := func(action, inner string) (int, string) {
		return 200, `<` + action + `Response xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><requestId>1</requestId>` + inner + `</` + action + `Response>`
	}
	tagName := func(form url.Values) string {
		for k, vs := range form {
			if strings.HasPrefix(k, "TagSpecification.1.Tag.1.Value") {
				return vs[0]
			}
		}
		return ""
	}
	filterName := func(form url.Values) string {
		for i := 1; i < 5; i++ {
			if form.Get(fmt.Sprintf("Filter.%d.Name", i)) == "tag:Name" {
				return form.Get(fmt.Sprintf("Filter.%d.Value.1", i))
			}
		}
		return ""
	}
	gateway := func(id string) string {
		att := ""
		if v.attached {
			att = `<item><vpcId>vpc-1</vpcId><state>attached</state></item>`
		}
		return `<vpnGatewayId>` + id + `</vpnGatewayId><state>available</state><type>ipsec.1</type><attachments>` + att + `</attachments>`
	}
	connection := func(id string, form url.Values) string {
		var tunnels strings.Builder
		var cidrs []string
		for k, vs := range form {
			if strings.HasSuffix(k, "TunnelInsideCidr") {
				cidrs = append(cidrs, vs[0])
			}
		}
		sort.Strings(cidrs)
		for i, c := range cidrs {
			fmt.Fprintf(&tunnels, `<item><outsideIpAddress>3.0.%s.%d</outsideIpAddress><tunnelInsideCidr>%s</tunnelInsideCidr></item>`, strings.TrimPrefix(id, "vpn-"), i+1, c)
		}
		return `<vpnConnectionId>` + id + `</vpnConnectionId><customerGatewayId>` + form.Get("CustomerGatewayId") + `</customerGatewayId><state>available</state><type>ipsec.1</type>` +
			`<options><staticRoutesOnly>false</staticRoutesOnly><tunnelOptionSet>` + tunnels.String() + `</tunnelOptionSet></options>`
	}
	q := func(action string, h func(url.Values) (int, string)) {
		f.query[action] = func(form url.Values) (int, string) {
			v.mu.Lock()
			defer v.mu.Unlock()
			return h(form)
		}
	}
	q("DescribeVpnGateways", func(form url.Values) (int, string) {
		if id, ok := v.gateways[filterName(form)]; ok {
			return ec2("DescribeVpnGateways", `<vpnGatewaySet><item>`+gateway(id)+`</item></vpnGatewaySet>`)
		}
		return ec2("DescribeVpnGateways", `<vpnGatewaySet/>`)
	})
	q("CreateVpnGateway", func(form url.Values) (int, string) {
		id := "vgw-1"
		v.gateways[tagName(form)] = id
		return ec2("CreateVpnGateway", `<vpnGateway>`+gateway(id)+`</vpnGateway>`)
	})
	q("AttachVpnGateway", func(url.Values) (int, string) {
		v.attached = true
		return ec2("AttachVpnGateway", `<attachment><vpcId>vpc-1</vpcId><state>attaching</state></attachment>`)
	})
	q("DescribeRouteTables", func(url.Values) (int, string) {
		return ec2("DescribeRouteTables", `<routeTableSet><item><routeTableId>rtb-1</routeTableId><vpcId>vpc-1</vpcId></item></routeTableSet>`)
	})
	q("EnableVgwRoutePropagation", func(url.Values) (int, string) {
		v.propagated = true
		return ec2("EnableVgwRoutePropagation", `<return>true</return>`)
	})
	q("CreateCustomerGateway", func(form url.Values) (int, string) {
		v.customers[tagName(form)] = form.Get("IpAddress")
		id := fmt.Sprintf("cgw-%d", len(v.customers))
		return ec2("CreateCustomerGateway", `<customerGateway><customerGatewayId>`+id+`</customerGatewayId><ipAddress>`+form.Get("IpAddress")+
			`</ipAddress><bgpAsn>`+form.Get("BgpAsn")+`</bgpAsn><state>available</state><type>ipsec.1</type></customerGateway>`)
	})
	q("DescribeVpnConnections", func(form url.Values) (int, string) {
		name := filterName(form)
		if c, ok := v.connections[name]; ok {
			return ec2("DescribeVpnConnections", `<vpnConnectionSet><item>`+connection(c.Get("id"), c)+`</item></vpnConnectionSet>`)
		}
		return ec2("DescribeVpnConnections", `<vpnConnectionSet/>`)
	})
	q("CreateVpnConnection", func(form url.Values) (int, string) {
		form.Set("id", fmt.Sprintf("vpn-%d", len(v.connections)+1))
		v.connections[tagName(form)] = form
		return ec2("CreateVpnConnection", `<vpnConnection>`+connection(form.Get("id"), form)+`</vpnConnection>`)
	})
	q("DeleteVpnConnection", func(form url.Values) (int, string) {
		for name, c := range v.connections {
			if c.Get("id") == form.Get("VpnConnectionId") {
				delete(v.connections, name)
			}
		}
		return ec2("DeleteVpnConnection", `<return>true</return>`)
	})
	q("DeleteCustomerGateway", func(url.Values) (int, string) {
		return ec2("DeleteCustomerGateway", `<return>true</return>`)
	})
	q("DetachVpnGateway", func(url.Values) (int, string) {
		v.attached = false
		return ec2("DetachVpnGateway", `<return>true</return>`)
	})
	q("DeleteVpnGateway", func(form url.Values) (int, string) {
		for name, id := range v.gateways {
			if id == form.Get("VpnGatewayId") {
				delete(v.gateways, name)
			}
		}
		return ec2("DeleteVpnGateway", `<return>true</return>`)
	})
	return v
}

const linkedManifest = `version: 1
services:
  gateway:
    source: apps/gateway
    provider: cloud-run
    needs: [api, db]
  api:
    source: go:services/api
    provider: ecs-fargate
    public: false
resources:
  db:
    kind: postgres
    provider: rds
links:
  vpn:
    between: [gcp, aws]
ignore:
  resources: [redis]
`

// linked is a deployment of linkedManifest, on fakes of both clouds.
type linked struct {
	c       *client
	base    string
	compute *fakeCompute
	vpn     *fakeVPN
	ecs     *fakeECS
	cloud   *fakeCloud
	schemes *[]string
}

func deployLinked(t *testing.T) *linked {
	t.Helper()
	google := newFakeGoogle(t)
	cloud := newFakeCloud(google.srv.URL)
	compute := &fakeCompute{next: cloud, resources: map[string]map[string]any{}}
	google.deploy = compute
	amazon := newFakeAWS(t)
	ecs := installFakeECS(t, amazon)
	vpn := installFakeVPN(amazon)
	var schemes []string
	createLB := amazon.query["CreateLoadBalancer"]
	amazon.query["CreateLoadBalancer"] = func(form url.Values) (int, string) {
		schemes = append(schemes, form.Get("Scheme"))
		return createLB(form)
	}
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	cfg.GitHub.APIURL = fakeGitHubServing(t).URL
	cfg.Analysis.WorkDir = t.TempDir()
	cfg.Clouds.GoogleEndpoint = google.srv.URL
	cfg.Clouds.AWSEndpoint = amazon.srv.URL
	cfg.Storage.URL = "file://" + t.TempDir()
	r := start(t, cfg)
	t.Cleanup(r.stop)

	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	signedIn(t, c)
	var proj struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Mono", "repository_url": "https://github.com/acme/mono"}, &proj)
	base := "/api/v1/projects/" + proj.ID
	analyze(t, cfg, c, proj.ID)
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections",
		map[string]string{"cloud": "gcp", "name": "Prod", "credential": serviceAccountKey(t, google.srv.URL+"/token")}, nil)
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections",
		map[string]any{"cloud": "aws", "name": "AWS", "credential": awsKey(awsKeyID), "settings": map[string]string{"region": "us-east-1"}}, nil)
	c.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": linkedManifest}, nil)

	var plan planBody
	c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]string{"environment": "production"}, &plan)
	waitPlanned(t, c, &plan)
	if plan.Status != "ready" || !plan.Executable || len(plan.Actions) != 4 {
		t.Fatalf("plan %+v", plan)
	}
	if d := approveAndWait(t, c, plan); d.Status != "succeeded" {
		t.Fatalf("deployment %+v", d)
	}
	return &linked{c: c, base: base, compute: compute, vpn: vpn, ecs: ecs, cloud: cloud, schemes: &schemes}
}

func TestDeploy_aLinkLetsCloudRunReachAPrivateECSServiceAndRDS(t *testing.T) {
	l := deployLinked(t)
	compute, vpn, ecs, cloud, schemes := l.compute, l.vpn, l.ecs, l.cloud, *l.schemes

	// Both sides of the link, joined: four tunnels, each with the key AWS
	// made its side with, and a BGP peer at AWS's end of it.
	compute.mu.Lock()
	vpn.mu.Lock()
	defer compute.mu.Unlock()
	defer vpn.mu.Unlock()
	if !vpn.attached || !vpn.propagated || len(vpn.customers) != 2 || len(vpn.connections) != 2 {
		t.Fatalf("aws side %+v", vpn)
	}
	for _, ip := range vpn.customers {
		if ip != "35.0.0.1" && ip != "35.0.0.2" {
			t.Fatalf("a customer gateway at %s, not Google's VPN gateway", ip)
		}
	}
	keys := map[string]bool{}
	for _, conn := range vpn.connections {
		for k, vs := range conn {
			if strings.HasSuffix(k, "PreSharedKey") {
				keys[vs[0]] = true
			}
		}
	}
	tunnels := 0
	var router map[string]any
	var peer map[string]any
	for key, res := range compute.resources {
		switch {
		case strings.HasPrefix(key, "vpnTunnels/"):
			tunnels++
			if !keys[res["sharedSecret"].(string)] {
				t.Fatalf("tunnel %s's secret isn't one AWS has", key)
			}
		case strings.HasPrefix(key, "routers/"):
			router = res
		case strings.HasPrefix(key, "externalVpnGateways/"):
			peer = res
		}
	}
	if tunnels != 4 || len(keys) != 4 {
		t.Fatalf("%d tunnels, %d keys", tunnels, len(keys))
	}
	if peers, _ := router["bgpPeers"].([]any); len(peers) != 4 || peers[0].(map[string]any)["peerAsn"].(float64) != 64512 {
		t.Fatalf("router %+v", router)
	}
	if ifaces, _ := peer["interfaces"].([]any); len(ifaces) != 4 || !strings.HasPrefix(ifaces[0].(map[string]any)["ipAddress"].(string), "3.0.") {
		t.Fatalf("peer gateway %+v", peer)
	}

	ecs.mu.Lock()
	defer ecs.mu.Unlock()
	// Google's network reaches the database and the internal load
	// balancer, which the API sits behind.
	for _, g := range []string{"db", "ilb"} {
		id := ""
		for name, gid := range ecs.groups {
			if strings.HasSuffix(name, "-"+g) {
				id = gid
			}
		}
		if !strings.Contains(strings.Join(ecs.ingress[id], ","), "10.128.0.0/9") {
			t.Fatalf("%s group %s lets in %v", g, id, ecs.ingress[id])
		}
	}
	// The API is private, so the one load balancer is the internal one.
	if len(ecs.lbs) != 1 || len(schemes) != 1 || schemes[0] != "internal" {
		t.Fatalf("load balancers %v made %v, want just the internal one", ecs.lbs, schemes)
	}

	// The gateway sends its private traffic into the default network.
	cloud.mu.Lock()
	defer cloud.mu.Unlock()
	for name, svc := range cloud.services {
		if !strings.Contains(name, "gateway") {
			continue
		}
		data, _ := json.Marshal(svc)
		if !strings.Contains(string(data), `"vpcAccess"`) || !strings.Contains(string(data), "PRIVATE_RANGES_ONLY") {
			t.Fatalf("gateway on cloud run %s", data)
		}
	}
}

func TestDeploy_aLinkIsTakenDownOnBothClouds(t *testing.T) {
	l := deployLinked(t)
	// Only the database stays: the services and the link go.
	l.c.must(http.StatusCreated, http.MethodPut, l.base+"/manifest", map[string]string{
		"yaml": "version: 1\nresources:\n  db: {kind: postgres, provider: rds}\nignore:\n  resources: [redis]\n",
	}, nil)
	var plan planBody
	l.c.must(http.StatusCreated, http.MethodPost, l.base+"/plans", map[string]string{"environment": "production"}, &plan)
	waitPlanned(t, l.c, &plan)
	if !plan.Executable {
		t.Fatalf("plan %+v", plan)
	}
	if d := approveAndWait(t, l.c, plan); d.Status != "succeeded" {
		t.Fatalf("deployment %+v", d)
	}
	l.compute.mu.Lock()
	defer l.compute.mu.Unlock()
	if len(l.compute.resources) != 0 || len(l.compute.deleted) != 7 {
		t.Fatalf("left on google cloud %v, deleted %v", l.compute.resources, l.compute.deleted)
	}
	// Tunnels go before the router and gateways they use.
	if !strings.HasPrefix(l.compute.deleted[0], "vpnTunnels/") || !strings.HasPrefix(l.compute.deleted[6], "vpnGateways/") {
		t.Fatalf("deleted in the order %v", l.compute.deleted)
	}
	l.vpn.mu.Lock()
	defer l.vpn.mu.Unlock()
	if len(l.vpn.connections) != 0 || len(l.vpn.gateways) != 0 || l.vpn.attached {
		t.Fatalf("left on aws %+v", l.vpn)
	}
}
