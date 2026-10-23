//go:build integration

package integration

import (
	"context"
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/planning"
	apiclient "github.com/xraph/loomplane/pkg/client"

	"github.com/joho/godotenv"
	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/vault/crypto"
	"github.com/xraph/vault/secret"
	vaultpg "github.com/xraph/vault/store/postgres"

	"github.com/xraph/loomplane/internal/clouds/digitalocean"
	"github.com/xraph/loomplane/internal/clouds/vercel"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

const liveOrg = "aorg_01m4smcd0fehg8k3050gr3y46b"
const liveDO = "conn_01m5d3vznqec392f2dvc7p9016"
const liveDNS = "conn_01m5ead3g2ee6bv3yspqs7gsba"
const liveTeam = "team_hLCTejWYCdvK7glFxF4qhH2z"
const liveZone = "shipbraid.dev"
const liveSize = "s-1vcpu-2gb"
const liveRegion = "nyc3"
const liveImage = "ubuntu-24-04-x64"

type liveHostIntent struct {
	RetainedTags []string                       `json:"retained_provider_tags,omitempty"`
	Operation    string                         `json:"operation"`
	Name         string                         `json:"name"`
	Attempted    bool                           `json:"attempted"`
	Receipt      *provider.InfrastructureServer `json:"receipt,omitempty"`
	Deleted      bool                           `json:"deleted"`
}
type liveDNSIntent struct {
	Attempted   bool                 `json:"attempted"`
	Transitions []provider.DNSRecord `json:"transitions,omitempty"`
	Desired     provider.DNSRecord   `json:"desired"`
	Receipt     *provider.DNSRecord  `json:"receipt,omitempty"`
	Deleted     bool                 `json:"deleted"`
}
type liveLedger struct {
	DatabaseCluster string               `json:"database_cluster,omitempty"`
	DatabaseOID     uint32               `json:"database_oid,omitempty"`
	Database        string               `json:"database,omitempty"`
	Version         int                  `json:"version"`
	RunID           string               `json:"run_id"`
	PreparedAt      time.Time            `json:"prepared_at"`
	StartedAt       time.Time            `json:"started_at"`
	Deadline        time.Time            `json:"deadline"`
	Hourly          float64              `json:"hourly_usd"`
	Account         string               `json:"account"`
	Zone            provider.DNSZone     `json:"zone"`
	Baseline        []provider.DNSRecord `json:"baseline"`
	Hosts           []liveHostIntent     `json:"hosts"`
	Records         []liveDNSIntent      `json:"records"`
	Proofs          []liveDNSIntent      `json:"proofs"`
	Evidence        map[string]any       `json:"evidence"`
	Cleaned         bool                 `json:"cleaned"`
}
type liveProviders struct {
	compute       liveComputeReader
	dns           liveDNSReader
	doCredential  provider.InfrastructureCredential
	dnsCredential provider.InfrastructureCredential
	doRaw, dnsRaw []byte
}

func liveWrite(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".live-*")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if _, err = file.Write(raw); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(name, path)
}
func liveRead(path string) (*liveLedger, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var ledger liveLedger
	if err = json.Unmarshal(raw, &ledger); err != nil {
		return nil, err
	}
	if ledger.Version != 1 || len(ledger.RunID) != 24 || !strings.HasPrefix(ledger.RunID, "lp-live-") || ledger.Zone.Name != liveZone || ledger.Zone.AccountID != liveTeam || ledger.Account == "" || len(ledger.Hosts) > 2 {
		return nil, fmt.Errorf("invalid ownership ledger")
	}
	if _, err = hex.DecodeString(strings.TrimPrefix(ledger.RunID, "lp-live-")); err != nil {
		return nil, fmt.Errorf("invalid run identity")
	}
	if !ledger.StartedAt.IsZero() && (ledger.Deadline.After(ledger.StartedAt.Add(6*time.Hour)) || !ledger.Deadline.After(ledger.StartedAt)) {
		return nil, fmt.Errorf("invalid deadline")
	}
	for i, h := range ledger.Hosts {
		if h.Name != fmt.Sprintf("%s-host%d", ledger.RunID, i+1) || h.Operation == "" {
			return nil, fmt.Errorf("invalid host intent")
		}
	}
	return &ledger, nil
}
func liveCredentials(ctx context.Context) (*liveProviders, error) {
	_ = godotenv.Load()
	cfg, err := config.Load(".")
	if err != nil {
		return nil, fmt.Errorf("load primary read configuration")
	}
	pg := pgdriver.New()
	if err = pg.Open(ctx, cfg.Database.URL); err != nil {
		return nil, fmt.Errorf("open primary read connection")
	}
	db, err := grove.Open(pg)
	if err != nil {
		return nil, fmt.Errorf("open primary read wrapper")
	}
	defer db.Close()
	key, err := security.DeriveKey(cfg.SecretKey, connections.VaultKeyLabel)
	if err != nil {
		return nil, fmt.Errorf("derive credential key")
	}
	defer clear(key)
	encryptor, err := crypto.NewEncryptor(key)
	if err != nil {
		return nil, fmt.Errorf("credential encryptor")
	}
	vault := connections.NewVaultSecrets(secret.NewService(vaultpg.New(db), encryptor, secret.WithAppID(connections.VaultAppID)))
	store := connections.NewPgStore(pgdriver.Unwrap(db))
	result := &liveProviders{compute: digitalocean.New(""), dns: vercel.New("")}
	for _, id := range []string{liveDO, liveDNS} {
		conn, readErr := store.GetConnection(ctx, liveOrg, id)
		if readErr != nil || conn.Status != connections.StatusReady {
			return nil, fmt.Errorf("selected connection is unavailable")
		}
		raw, revealErr := vault.Reveal(ctx, liveOrg, id)
		if revealErr != nil {
			return nil, fmt.Errorf("read selected credential")
		}
		if id == liveDO {
			if conn.Cloud != "digitalocean" {
				clear(raw)
				return nil, fmt.Errorf("compute connection identity mismatch")
			}
			account := conn.Details["team_uuid"]
			if account == "" {
				account = conn.Details["account_uuid"]
			}
			result.doRaw = raw
			result.doCredential = provider.InfrastructureCredential{Provider: "digitalocean", AccountID: account, Token: string(raw)}
		} else {
			if conn.Cloud != "vercel" || conn.Details["team_id"] != liveTeam {
				clear(raw)
				return nil, fmt.Errorf("DNS connection identity mismatch")
			}
			result.dnsRaw = raw
			result.dnsCredential = provider.InfrastructureCredential{Provider: "vercel", AccountID: liveTeam, TeamID: liveTeam, Token: strings.TrimSpace(string(raw))}
		}
	}
	return result, nil
}
func (p *liveProviders) close() {
	clear(p.doRaw)
	clear(p.dnsRaw)
	p.doCredential.Token = ""
	p.dnsCredential.Token = ""
}
func (p *liveProviders) inventory(ctx context.Context) (provider.DNSZone, []provider.DNSRecord, error) {
	zone, err := p.dns.GetZone(ctx, p.dnsCredential, liveZone)
	if err != nil {
		return zone, nil, err
	}
	if zone.Name != liveZone || zone.AccountID != liveTeam || !zone.Verified {
		return zone, nil, fmt.Errorf("selected zone changed")
	}
	records, err := p.dns.ListRecords(ctx, p.dnsCredential, zone)
	return zone, records, err
}
func liveEqualInventory(a, b []provider.DNSRecord) bool {
	a = slices.Clone(a)
	b = slices.Clone(b)
	slices.SortFunc(a, func(x, y provider.DNSRecord) int { return strings.Compare(x.ID, y.ID) })
	slices.SortFunc(b, func(x, y provider.DNSRecord) int { return strings.Compare(x.ID, y.ID) })
	return slices.Equal(a, b)
}
func (p *liveProviders) price(ctx context.Context) (float64, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.digitalocean.com/v2/sizes?per_page=200", nil)
	if err != nil {
		return 0, err
	}
	request.Header.Set("Authorization", "Bearer "+p.doCredential.Token)
	response, err := (&http.Client{Timeout: 30 * time.Second}).Do(request)
	if err != nil {
		return 0, fmt.Errorf("size lookup failed")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return 0, fmt.Errorf("size lookup HTTP %d", response.StatusCode)
	}
	var body struct {
		Sizes []struct {
			Slug                string
			Available           bool
			Memory, VCPUs, Disk int
			Hourly              float64 `json:"price_hourly"`
			Regions             []string
		}
	}
	if err = json.NewDecoder(response.Body).Decode(&body); err != nil {
		return 0, fmt.Errorf("size lookup response invalid")
	}
	for _, size := range body.Sizes {
		if size.Slug == liveSize && size.Available && size.Memory == 2048 && size.VCPUs == 1 && size.Disk == 50 && slices.Contains(size.Regions, liveRegion) && size.Hourly > 0 && size.Hourly*12 < 1 {
			return size.Hourly, nil
		}
	}
	return 0, fmt.Errorf("approved size, region or budget unavailable")
}
func liveHostTags(intent liveHostIntent) []string {
	spec := provider.ServerSpec{OperationID: intent.Operation, Name: intent.Name, Region: liveRegion, Size: liveSize, Image: liveImage}
	raw, _ := json.Marshal(spec)
	sum := sha256.Sum256(raw)
	return []string{provider.ServerOperationTag(intent.Operation), "loomplane-spec-" + hex.EncodeToString(sum[:])}
}
func liveHostMatches(account string, intent liveHostIntent, actual provider.InfrastructureServer) bool {
	tags := liveHostTags(intent)
	return actual.ID != "" && actual.AccountID == account && actual.Name == intent.Name && actual.Region == liveRegion && actual.Size == liveSize && slices.Contains(actual.Tags, tags[0]) && slices.Contains(actual.Tags, tags[1]) && (intent.Receipt == nil || intent.Receipt.ID == actual.ID)
}
func liveResolveHost(account string, intent liveHostIntent, all []provider.InfrastructureServer) (*provider.InfrastructureServer, error) {
	var found *provider.InfrastructureServer
	for _, actual := range all {
		if actual.Name == intent.Name || slices.Contains(actual.Tags, provider.ServerOperationTag(intent.Operation)) || (intent.Receipt != nil && actual.ID == intent.Receipt.ID) {
			if found != nil || !liveHostMatches(account, intent, actual) {
				return nil, fmt.Errorf("host ownership drift")
			}
			snapshot := actual
			found = &snapshot
		}
	}
	if found == nil && intent.Attempted && intent.Receipt == nil {
		return nil, fmt.Errorf("unresolved create response; absence does not prove no create")
	}
	return found, nil
}
func liveAllowedDNSIntent(l *liveLedger, intent liveDNSIntent) bool {
	if intent.Desired.ZoneID != l.Zone.ID {
		return false
	}
	for index := range 3 {
		child := fmt.Sprintf("%s-app%d", l.RunID, index)
		if intent.Desired.Name == child && intent.Desired.Type == "A" && strings.HasPrefix(intent.Desired.Owner, "loomplane:route:") {
			return true
		}
		if intent.Desired.Name == "_loomplane."+child && intent.Desired.Type == "TXT" && intent.Desired.Owner == fmt.Sprintf("loomplane:proof:%s:%d", l.RunID, index) && strings.HasPrefix(intent.Desired.Value, "loomplane=zone_") && len(intent.Transitions) == 0 {
			return true
		}
	}
	return false
}

func liveResolveRecord(zone provider.DNSZone, intent liveDNSIntent, all []provider.DNSRecord) (*provider.DNSRecord, error) {
	var found *provider.DNSRecord
	for _, actual := range all {
		if actual.Name != intent.Desired.Name {
			continue
		}
		want := intent.Desired
		want.ID = actual.ID
		matches := actual == want
		for _, transition := range intent.Transitions {
			transition.ID = actual.ID
			if actual == transition {
				matches = true
			}
		}
		if !matches || actual.ZoneID != zone.ID || found != nil {
			return nil, fmt.Errorf("DNS ownership or value drift")
		}
		snapshot := actual
		found = &snapshot
	}
	if found == nil && intent.Attempted && intent.Receipt == nil {
		return nil, fmt.Errorf("DNS create outcome unresolved")
	}
	return found, nil
}

func TestAcceptanceSharedCloudLive(t *testing.T) {
	path := os.Getenv("LOOMPLANE_SHARED_LIVE_STATE")
	if path == "" {
		t.Skip("run tests/acceptance/shared-clouds/live.py")
	}
	mode := os.Getenv("LOOMPLANE_SHARED_LIVE_MODE")
	if mode != "prepare" && mode != "inspect" && mode != "cleanup" && mode != "run" {
		t.Fatal("invalid live mode")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	providers, err := liveCredentials(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer providers.close()
	if mode == "prepare" {
		livePrepare(ctx, t, path, providers)
		return
	}
	ledger, err := liveRead(path)
	if err != nil {
		t.Fatal(err)
	}
	if ledger.Account != providers.doCredential.AccountID {
		t.Fatal("compute account changed")
	}
	switch mode {
	case "inspect":
		liveInspect(ctx, t, path, ledger, providers, false)
	case "cleanup":
		if os.Getenv("LOOMPLANE_SHARED_LIVE_QUIESCED") != ledger.RunID {
			t.Fatal("cleanup requires supervisor quiescence")
		}
		liveInspect(ctx, t, path, ledger, providers, true)
		if err = liveDropDatabase(ctx, ledger); err != nil {
			t.Fatal(err)
		}
	case "run":
		liveRun(t, path, ledger, providers)
	}
}
func livePrepare(ctx context.Context, t *testing.T, path string, p *liveProviders) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("prepare refuses existing ledger")
	}
	if _, err := p.compute.ListServers(ctx, p.doCredential); err != nil {
		t.Fatal(err)
	}
	hourly, err := p.price(ctx)
	if err != nil {
		t.Fatal(err)
	}
	zone, records, err := p.inventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	run := os.Getenv("LOOMPLANE_SHARED_LIVE_ID")
	ledger := liveLedger{Version: 1, RunID: run, PreparedAt: time.Now().UTC(), Hourly: hourly, Account: p.doCredential.AccountID, Zone: zone, Baseline: records, Evidence: map[string]any{}}
	if err = liveWrite(path, ledger); err != nil {
		t.Fatal(err)
	}
	if _, err = liveRead(path); err != nil {
		t.Fatal(err)
	}
}
func liveInspect(ctx context.Context, t *testing.T, path string, l *liveLedger, p *liveProviders, cleanup bool) {
	t.Helper()
	if err := liveInspectResources(ctx, path, l, p, cleanup); err != nil {
		t.Fatal(err)
	}
}

func liveInspectResources(ctx context.Context, path string, l *liveLedger, p *liveProviders, cleanup bool) error {
	var failures []error
	l.Cleaned = false
	all, computeErr := p.compute.ListServers(ctx, p.doCredential)
	if computeErr != nil {
		failures = append(failures, fmt.Errorf("compute inventory unavailable"))
	}
	zone, records, dnsErr := p.inventory(ctx)
	if dnsErr == nil && !reflect.DeepEqual(zone, l.Zone) {
		dnsErr = fmt.Errorf("zone identity or nameservers changed")
	}
	if dnsErr != nil {
		failures = append(failures, fmt.Errorf("DNS inventory or identity unavailable"))
	} else {
		for _, intents := range [][]liveDNSIntent{l.Records, l.Proofs} {
			for i := range intents {
				intent := &intents[i]
				baselineConflict := slices.ContainsFunc(l.Baseline, func(record provider.DNSRecord) bool { return record.Name == intent.Desired.Name })
				if !liveAllowedDNSIntent(l, *intent) || baselineConflict {
					failures = append(failures, fmt.Errorf("unsafe DNS cleanup intent"))
					continue
				}
				actual, resolveErr := liveResolveRecord(zone, *intent, records)
				if resolveErr != nil {
					failures = append(failures, resolveErr)
					continue
				}
				if actual == nil {
					intent.Deleted = true
					continue
				}
				intent.Receipt = actual
				if err := liveWrite(path, l); err != nil {
					return err
				}
				if cleanup {
					if err := p.dns.DeleteRecord(ctx, p.dnsCredential, provider.DNSDeleteRequest{Zone: zone, Expected: *actual}); err != nil {
						failures = append(failures, fmt.Errorf("exact DNS deletion unresolved"))
					}
				}
			}
		}
	}
	if computeErr == nil {
		for i := range l.Hosts {
			intent := &l.Hosts[i]
			actual, resolveErr := liveResolveHost(l.Account, *intent, all)
			if resolveErr != nil {
				failures = append(failures, resolveErr)
				continue
			}
			if actual == nil && intent.Receipt != nil {
				checked, getErr := p.compute.GetServer(ctx, p.doCredential, intent.Receipt.ID)
				if errors.Is(getErr, provider.ErrInfrastructureNotFound) {
					intent.Deleted = true
					continue
				}
				if getErr != nil || !liveHostMatches(l.Account, *intent, checked) {
					failures = append(failures, fmt.Errorf("exact host absence or identity unverified"))
					continue
				}
				actual = &checked
			}
			if actual == nil {
				intent.Deleted = true
				continue
			}
			intent.Receipt = actual
			if err := liveWrite(path, l); err != nil {
				return err
			}
			if !cleanup {
				continue
			}
			if err := p.compute.DeleteServer(ctx, p.doCredential, actual.ID); err != nil && !errors.Is(err, provider.ErrInfrastructureNotFound) {
				failures = append(failures, fmt.Errorf("exact host deletion unresolved"))
				continue
			}
			for {
				_, err := p.compute.GetServer(ctx, p.doCredential, actual.ID)
				if errors.Is(err, provider.ErrInfrastructureNotFound) {
					intent.Deleted = true
					break
				}
				if err != nil {
					failures = append(failures, fmt.Errorf("host absence is unverified"))
					break
				}
				select {
				case <-ctx.Done():
					return fmt.Errorf("host deletion deadline exceeded")
				case <-time.After(time.Second):
				}
			}
		}
	}
	if cleanup && dnsErr == nil {
		after, remaining, readErr := p.inventory(ctx)
		if readErr != nil || !reflect.DeepEqual(after, l.Zone) || !liveEqualInventory(l.Baseline, remaining) {
			failures = append(failures, fmt.Errorf("surviving DNS inventory differs or is unverified"))
		} else {
			for _, intents := range [][]liveDNSIntent{l.Records, l.Proofs} {
				for i := range intents {
					intents[i].Deleted = true
				}
			}
		}
	}
	if cleanup && len(failures) == 0 {
		l.Cleaned = true
	}
	if err := liveWrite(path, l); err != nil {
		return err
	}
	return errors.Join(failures...)
}

func liveRun(t *testing.T, path string, l *liveLedger, p *liveProviders) {
	t.Helper()
	if os.Getenv("LOOMPLANE_SHARED_LIVE_APPROVAL") != l.RunID || !l.StartedAt.IsZero() || l.Cleaned {
		t.Fatal("run needs exact approval and a fresh unused prepared ledger; inspect or cleanup interrupted runs")
	}
	ctx := t.Context()
	hourly, err := p.price(ctx)
	if err != nil || hourly != l.Hourly {
		t.Fatal("price changed; prepare a new scope")
	}
	zone, baseline, err := p.inventory(ctx)
	if err != nil || !reflect.DeepEqual(zone, l.Zone) || !liveEqualInventory(l.Baseline, baseline) {
		t.Fatal("DNS baseline changed; prepare a new scope")
	}
	if _, err = p.compute.ListServers(ctx, p.doCredential); err != nil {
		t.Fatal(err)
	}
	// The supervisor records this deadline before the child can create anything.
	deadline, err := time.Parse(time.RFC3339Nano, os.Getenv("LOOMPLANE_SHARED_LIVE_DEADLINE"))
	if err != nil || !deadline.After(time.Now()) || deadline.After(time.Now().Add(6*time.Hour)) {
		t.Fatal("invalid supervisor deadline")
	}
	l.StartedAt = time.Now().UTC()
	l.Deadline = deadline
	record := func() {
		t.Helper()
		if saveErr := liveWrite(path, l); saveErr != nil {
			t.Fatal(saveErr)
		}
	}
	record()
	bound := func() {
		t.Helper()
		if !time.Now().Before(l.Deadline) {
			t.Fatal("live deadline reached")
		}
	}
	observer := &livePatchObserver{delegate: http.DefaultTransport, journal: filepath.Join(filepath.Dir(path), "dns-cutover.json")}
	originalTransport := http.DefaultTransport
	http.DefaultTransport = observer
	defer func() { http.DefaultTransport = originalTransport }()
	cfg := config.Defaults()
	secretKey := make([]byte, 32)
	if _, err = crand.Read(secretKey); err != nil {
		t.Fatal("isolated encryption key generation failed")
	}
	cfg.SecretKey = hex.EncodeToString(secretKey)
	clear(secretKey)
	cfg.Database.URL = liveDatabase(t, path, l)
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	cfg.GitHub.APIURL = fakeGitHubServing(t).URL
	cfg.Analysis.WorkDir = t.TempDir()
	cfg.Storage.URL = "file://" + t.TempDir()
	cfg.Clouds.EnrollmentURL = os.Getenv("LOOMPLANE_SHARED_LIVE_CALLBACK")
	if !strings.HasPrefix(cfg.Clouds.EnrollmentURL, "https://") || !strings.HasSuffix(cfg.Clouds.EnrollmentURL, ".trycloudflare.com") {
		t.Fatal("invalid approved callback origin")
	}
	running := start(t, cfg)
	defer running.stop()
	c := &client{t: t, base: running.srv.URL, key: cfg.Identity.PublishableKey}
	var auth struct {
		SessionToken string `json:"session_token"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/signup", map[string]string{"email": "live@example.test", "password": "Correct-horse-battery-9"}, &auth)
	c.token = auth.SessionToken
	var org struct{ ID string }
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": l.RunID, "slug": l.RunID}, &org)
	c.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, nil)
	l.Evidence["org_id"] = org.ID
	callbackPaths := []string{}
	policyPath := filepath.Join(filepath.Dir(path), "enrollment.json")
	policy := func() {
		t.Helper()
		if writeErr := liveWrite(policyPath, map[string]any{"upstream": running.srv.URL, "paths": callbackPaths, "deadline": min(float64(l.Deadline.Unix()), float64(l.StartedAt.Add(45*time.Minute).Unix()))}); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	policy()
	defer func() { callbackPaths = nil; policy() }()
	projects := make([]string, 0, 3)
	for index := range 3 {
		var project struct{ ID string }
		c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": fmt.Sprintf("%s-app%d", l.RunID, index), "repository_url": "https://github.com/acme/mono"}, &project)
		projects = append(projects, project.ID)
	}
	l.Evidence["projects"] = projects
	record()
	var cloud cloudprofiles.Profile
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects/"+projects[0]+"/clouds", cloudprofiles.CreateInput{Name: l.RunID}, &cloud)
	cloudBase := "/api/v1/clouds/" + cloud.ID
	for _, project := range projects[1:] {
		c.must(http.StatusOK, http.MethodPut, cloudBase+"/projects/"+project, map[string]any{"target_ids": []string{}}, nil)
	}
	connectionIDs := map[string]string{}
	for _, providerName := range []string{"digitalocean", "vercel"} {
		raw := p.doRaw
		settings := map[string]string{}
		if providerName == "vercel" {
			raw = p.dnsRaw
			settings["team_id"] = liveTeam
		}
		var connection struct{ ID, Status string }
		c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections", map[string]any{"cloud": providerName, "name": l.RunID + "-" + providerName, "credential": string(raw), "settings": settings}, &connection)
		if connection.Status != "ready" {
			t.Fatal("isolated provider connection not ready")
		}
		connectionIDs[providerName] = connection.ID
	}
	pools := make([]cloudinfra.Pool, 0, 2)
	hosts := make([]combinedHost, 0, 2)
	keys := make([][]byte, 0, 2)
	runtimeIDs := make([]string, 0, 2)
	connectionService, err := forge.InjectType[*connections.Service](running.app.(forge.App).Container())
	if err != nil {
		t.Fatal(err)
	}
	for index := range 2 {
		bound()
		var operation cloudinfra.Operation
		name := fmt.Sprintf("%s-host%d", l.RunID, index+1)
		c.must(http.StatusOK, http.MethodPost, cloudBase+"/servers/preview", cloudinfra.ServerInput{Kind: "create", Name: name, ConnectionID: connectionIDs["digitalocean"], Region: liveRegion, Size: liveSize, Image: liveImage, CloudRevision: cloud.Revision}, &operation)
		if operation.AccountID != l.Account || operation.EstimatedHourlyUSD != l.Hourly || operation.Input.Name != name || operation.Input.Size != liveSize || operation.Input.Region != liveRegion || operation.Input.Image != liveImage {
			t.Fatal("server preview exceeds approved scope")
		}
		intent := liveHostIntent{Operation: operation.ID, Name: name}
		intent.RetainedTags = liveHostTags(intent)
		l.Hosts = append(l.Hosts, intent)
		record()
		callbackPaths = append(callbackPaths, "/api/v1/infrastructure/enroll/"+org.ID+"/"+operation.ID)
		policy()
		l.Hosts[index].Attempted = true
		record()
		bound()
		c.must(http.StatusAccepted, http.MethodPost, cloudBase+"/infrastructure-operations/"+operation.ID+"/apply", cloudinfra.ApplyInput{Review: operation.Review}, &operation)
		for readyBy := time.Now().Add(20 * time.Minute); ; time.Sleep(2 * time.Second) {
			bound()
			c.must(http.StatusOK, http.MethodGet, cloudBase+"/infrastructure-operations/"+operation.ID, nil, &operation)
			if operation.Enrolled && slices.Contains(callbackPaths, "/api/v1/infrastructure/enroll/"+org.ID+"/"+operation.ID) {
				callbackPaths = slices.DeleteFunc(callbackPaths, func(value string) bool { return strings.HasSuffix(value, "/"+operation.ID) })
				policy()
				if index == 1 {
					l.Evidence["enrollment_complete"] = true
					record()
				}
			}
			if operation.ResourceID != "" && l.Hosts[index].Receipt == nil {
				actual, getErr := p.compute.GetServer(ctx, p.doCredential, operation.ResourceID)
				if getErr != nil || !liveHostMatches(l.Account, l.Hosts[index], actual) {
					t.Fatal("created host receipt mismatch")
				}
				l.Hosts[index].Receipt = &actual
				record()
			}
			if operation.Status == "succeeded" {
				break
			}
			if operation.Status == "failed" || time.Now().After(readyBy) {
				t.Fatal("managed bootstrap did not complete; inspect retained ownership")
			}
		}
		if !operation.Enrolled || l.Hosts[index].Receipt == nil {
			t.Fatal("bootstrap lacks enrollment or immutable provider receipt")
		}
		callbackPaths = slices.DeleteFunc(callbackPaths, func(value string) bool { return strings.HasSuffix(value, "/"+operation.ID) })
		policy()
		var listing struct{ Servers []cloudinfra.Server }
		c.must(http.StatusOK, http.MethodGet, cloudBase+"/servers", nil, &listing)
		var managed cloudinfra.Server
		for _, candidate := range listing.Servers {
			if candidate.ID == operation.ServerID {
				managed = candidate
			}
		}
		if managed.RuntimeConnectionID == "" || managed.Health.DockerID == "" {
			t.Fatal("missing verified runtime identity")
		}
		conn, key, readErr := connectionService.Credential(ctx, org.ID, managed.RuntimeConnectionID)
		if readErr != nil {
			t.Fatal("read isolated runtime credential")
		}
		defer clear(key)
		hosts = append(hosts, combinedHost{User: "loomplane", Address: conn.Details["host"], HostKey: conn.Details["host_key"], Daemon: managed.Health.DockerID})
		keys = append(keys, key)
		runtimeIDs = append(runtimeIDs, managed.RuntimeConnectionID)
		var pool cloudinfra.Pool
		c.must(http.StatusOK, http.MethodPost, cloudBase+"/pools", cloudinfra.PoolInput{Name: name, Members: []cloudinfra.PoolMember{{ServerID: managed.ID, CPUMillis: 1000, MemoryBytes: 1 << 30}}}, &pool)
		pools = append(pools, pool)
	}
	l.Evidence["enrollment_complete"] = true
	record()
	namespaces := make([]cloudinfra.Zone, 0, len(projects))
	writer, ok := p.dns.(provider.DNSAdapter)
	if !ok {
		t.Fatal("live DNS adapter cannot publish ownership challenges")
	}
	for index, project := range projects {
		bound()
		var namespace cloudinfra.Zone
		name := fmt.Sprintf("%s-app%d.%s", l.RunID, index, liveZone)
		c.must(http.StatusCreated, http.MethodPost, "/api/v1/dns-zones", cloudinfra.ZoneInput{Name: name, Mode: "managed", ConnectionID: connectionIDs["vercel"], ProjectIDs: []string{project}}, &namespace)
		if namespace.Name != name || namespace.ProviderZone.ID != l.Zone.ID || namespace.ProviderZone.Name != liveZone || namespace.ProofName != "_loomplane."+name || !namespace.VerifiedAt.IsZero() {
			t.Fatal("managed namespace or public challenge widened")
		}
		intent := liveDNSIntent{Desired: provider.DNSRecord{ZoneID: l.Zone.ID, Name: strings.TrimSuffix(namespace.ProofName, "."+liveZone), Type: "TXT", Value: namespace.ProofValue, TTL: 60, Owner: fmt.Sprintf("loomplane:proof:%s:%d", l.RunID, index)}}
		if !liveAllowedDNSIntent(l, intent) {
			t.Fatal("proof record outside approved scope")
		}
		l.Proofs = append(l.Proofs, intent)
		record()
		bound()
		l.Proofs[index].Attempted = true
		record()
		receipt, writeErr := writer.UpsertRecord(ctx, p.dnsCredential, provider.DNSUpsertRequest{Zone: l.Zone, Record: intent.Desired})
		if writeErr != nil {
			t.Fatal("public challenge write unresolved; retained cleanup intent")
		}
		l.Proofs[index].Receipt = &receipt
		record()
		for until := time.Now().Add(5 * time.Minute); ; time.Sleep(2 * time.Second) {
			bound()
			if c.do(http.MethodPost, "/api/v1/dns-zones/"+namespace.ID+"/verify", nil, &namespace) == http.StatusOK {
				break
			}
			if time.Now().After(until) {
				t.Fatal("public namespace control not established")
			}
		}
		namespaces = append(namespaces, namespace)
	}
	liveLifecycle(t, c, cfg, l, p, cloud, pools, hosts, keys, runtimeIDs, projects, namespaces, observer, record, bound)
}

func liveLifecycle(t *testing.T, c *client, cfg config.Config, l *liveLedger, p *liveProviders, cloud cloudprofiles.Profile, pools []cloudinfra.Pool, hosts []combinedHost, keys [][]byte, runtimeIDs, projects []string, namespaces []cloudinfra.Zone, observer *livePatchObserver, record, bound func()) {
	t.Helper()
	targets := make([]cloudprofiles.Target, 0, len(projects))
	bindings := make([]planning.Binding, 0, len(projects))
	routes := make([]cloudinfra.Route, 0, len(projects))
	variables := make([]string, 0, len(projects))
	markers := make([]string, 0, len(projects))
	hostIndices := []int{0, 0, 1}
	binding := func(project string) planning.Binding {
		t.Helper()
		var listing struct{ Bindings []planning.Binding }
		c.must(http.StatusOK, http.MethodGet, "/api/v1/projects/"+project+"/bindings?environment=production", nil, &listing)
		for _, item := range listing.Bindings {
			if item.Name == "web" {
				return item
			}
		}
		t.Fatal("web binding missing")
		return planning.Binding{}
	}
	deploy := func(project, target, health string) {
		t.Helper()
		bound()
		base := "/api/v1/projects/" + project
		c.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": combinedManifest(target, health)}, nil)
		var plan planBody
		c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]string{"environment": "production"}, &plan)
		waitPlanned(t, c, &plan)
		if !plan.Executable {
			t.Fatal("live manifest plan is not executable")
		}
		l.Evidence["plan-"+project] = plan.ID
		record()
		c.must(http.StatusOK, http.MethodPost, "/api/v1/plans/"+plan.ID+"/approve", map[string]string{"revision": plan.Revision}, nil)
		for until := time.Now().Add(5 * time.Minute); ; time.Sleep(time.Second) {
			bound()
			var deployment deploymentBody
			status := c.do(http.MethodGet, "/api/v1/plans/"+plan.ID+"/deployment", nil, &deployment)
			if status == http.StatusOK && deployment.Status == "succeeded" {
				return
			}
			if deployment.Status == "failed" || deployment.Status == "canceled" || time.Now().After(until) {
				t.Fatal("live deployment did not succeed")
			}
		}
	}
	verify := func(index, host int, marker string) {
		t.Helper()
		route := routes[index]
		if err := combinedHTTPS(t.Context(), route.Input.Hostname, hosts[host].Address, marker, route.Binding.ContainerID); err != nil {
			t.Fatal(err)
		}
	}
	verifyPublic := func(index, host int, marker string) {
		t.Helper()
		name := routes[index].Input.Hostname
		addresses, err := net.DefaultResolver.LookupHost(t.Context(), name)
		if err != nil || len(addresses) != 1 || addresses[0] != hosts[host].Address {
			t.Fatal("public DNS does not resolve to intended backend")
		}
		verify(index, host, marker)
	}
	for index, project := range projects {
		base := "/api/v1/projects/" + project
		analyze(t, cfg, c, project)
		var envs struct{ Environments []struct{ ID, Name string } }
		c.must(http.StatusOK, http.MethodGet, base+"/environments", nil, &envs)
		environment := ""
		for _, item := range envs.Environments {
			if item.Name == "production" {
				environment = item.ID
			}
		}
		var target cloudprofiles.Target
		c.must(http.StatusCreated, http.MethodPost, base+"/clouds/"+cloud.ID+"/targets", cloudprofiles.CreateTargetInput{Name: fmt.Sprintf("app%d", index), Provider: "server", Runtime: "server", PoolID: pools[hostIndices[index]].ID}, &target)
		targets = append(targets, target)
		var preview cloudprofiles.Preview
		c.must(http.StatusOK, http.MethodPost, base+"/environments/"+environment+"/cloud/preview", apiclient.EnvironmentCloudInput{CloudID: cloud.ID}, &preview)
		if result := waitCloudAPI(t, c, base, applyCloudAPI(t, c, base, &preview, "assign-"+project).ID); result.Status != cloudprofiles.SyncSucceeded {
			t.Fatal("live assignment failed")
		}
		marker := l.RunID + "-" + project + "-release-one"
		markers = append(markers, marker)
		var variable struct{ ID string }
		c.must(http.StatusCreated, http.MethodPost, base+"/variables", map[string]any{"key": "WHOAMI_NAME", "value": marker, "environment": "production"}, &variable)
		variables = append(variables, variable.ID)
		deploy(project, target.Name, "/")
		bindings = append(bindings, binding(project))
		var operation cloudinfra.RouteOperation
		c.must(http.StatusOK, http.MethodPost, base+"/domain-routes/preview", cloudinfra.RouteInput{ZoneID: namespaces[index].ID, CloudID: cloud.ID, EnvironmentID: environment, Service: "web", Hostname: fmt.Sprintf("%s-app%d.%s", l.RunID, index, liveZone), Port: 80}, &operation)
		l.Records = append(l.Records, liveDNSIntent{Desired: operation.DesiredRecord})
		record()
		bound()
		l.Records[index].Attempted = true
		record()
		c.must(http.StatusAccepted, http.MethodPost, base+"/route-operations/"+operation.ID+"/apply", cloudinfra.ApplyInput{Review: operation.Review}, &operation)
		for until := time.Now().Add(10 * time.Minute); ; time.Sleep(2 * time.Second) {
			bound()
			c.must(http.StatusOK, http.MethodGet, base+"/route-operations/"+operation.ID, nil, &operation)
			if operation.Receipt != nil {
				l.Records[index].Receipt = operation.Receipt
				record()
			}
			if operation.Status == "succeeded" {
				break
			}
			if operation.Status == "waiting" {
				status := c.do(http.MethodPost, base+"/route-operations/"+operation.ID+"/retry", nil, nil)
				if status != http.StatusAccepted && status != http.StatusConflict {
					t.Fatal("live route retry failed")
				}
			}
			if operation.Status == "failed" || time.Now().After(until) {
				t.Fatal("public route did not become ready")
			}
		}
		routes = append(routes, operation.Route)
		l.Evidence["route-"+project] = operation.Route
		record()
		verifyPublic(index, hostIndices[index], marker)
	}
	l.Evidence["three_project_public_https"] = "passed"
	record()
	first := projects[0]
	base := "/api/v1/projects/" + first
	var preview cloudprofiles.Preview
	c.must(http.StatusOK, http.MethodPost, base+"/cloud-changes/preview", cloudprofiles.ChangeInput{CloudID: cloud.ID, TargetID: targets[0].ID, Revision: targets[0].Revision, Provider: "server", Runtime: "server", PoolID: pools[1].ID}, &preview)
	changed := 0
	for _, environment := range preview.Environments {
		if environment.Summary.Create+environment.Summary.Update+environment.Summary.Replace+environment.Summary.Delete+environment.Summary.Adopt > 0 {
			changed++
			if environment.ProjectID != first || !reflect.DeepEqual(environment.Services, []string{"web"}) {
				t.Fatal("migration affects unrelated service")
			}
		}
	}
	if changed != 1 {
		t.Fatal("unexpected migration participants")
	}
	// Persist both exact values before the product can PATCH DNS. Cleanup may
	// reconcile either receipt, including Vercel's rotated record ID.
	destination := l.Records[0].Desired
	destination.Value = hosts[1].Address
	l.Records[0].Transitions = []provider.DNSRecord{destination}
	record()
	_, beforeDNS, err := p.inventory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	oldRecord, err := liveResolveRecord(l.Zone, l.Records[0], beforeDNS)
	if err != nil || oldRecord == nil || oldRecord.Value != hosts[0].Address {
		t.Fatal("source DNS receipt drift")
	}
	observer.mutex.Lock()
	observer.path = "/v1/domains/records/" + oldRecord.ID
	observer.destination = hosts[1].Address
	observer.mutex.Unlock()
	sourceContainer := routes[0].Binding.ContainerID
	sourceConnection := combinedSSH(t, hosts[0], keys[0])
	observerContext, cancelObserver := context.WithCancel(t.Context())
	sourceDone := make(chan struct{})
	var observations []combinedSourceObservation
	var observeErr error
	sourceHostname, sourceAddress, sourceMarker := routes[0].Input.Hostname, hosts[0].Address, markers[0]
	go func() {
		defer close(sourceDone)
		observations, observeErr = liveObserveSource(observerContext, observer, func(until time.Time) ([]combinedSourceObservation, error) {
			return combinedObserveSource(observerContext, sourceConnection, sourceHostname, sourceAddress, sourceMarker, sourceContainer, until)
		}, time.Duration(oldRecord.TTL)*time.Second)
	}()
	defer func() { cancelObserver(); <-sourceDone }()
	migration := applyCloudAPI(t, c, base, &preview, "live-current-release")
	l.Evidence["migration"] = migration.ID
	record()
	for until := time.Now().Add(10 * time.Minute); ; time.Sleep(time.Second) {
		bound()
		verify(1, 0, markers[1])
		verify(2, 1, markers[2])
		c.must(http.StatusOK, http.MethodGet, base+"/cloud-syncs/"+migration.ID, nil, migration)
		_, inventory, readErr := p.inventory(t.Context())
		if readErr != nil {
			t.Fatal(readErr)
		}
		current, resolveErr := liveResolveRecord(l.Zone, l.Records[0], inventory)
		if resolveErr != nil || current == nil {
			t.Fatal("migration DNS receipt unresolved")
		}
		l.Records[0].Receipt = current
		record()
		if migration.Status == cloudprofiles.SyncSucceeded {
			at, captureErr := observer.completion()
			if captureErr != nil || at.IsZero() || time.Now().Before(at.Add(time.Duration(oldRecord.TTL)*time.Second)) {
				t.Fatal("migration bypassed independent provider completion plus TTL bound")
			}
			if current.Value != hosts[1].Address {
				t.Fatal("migration DNS does not point to destination")
			}
			break
		}
		if migration.Status == cloudprofiles.SyncFailed || time.Now().After(until) {
			t.Fatal("live migration did not succeed")
		}
	}
	<-sourceDone
	if observeErr != nil {
		t.Fatal(observeErr)
	}
	at, captureErr := observer.completion()
	if captureErr != nil || len(observations) < 2 || observations[len(observations)-1].At.Before(at.Add(time.Duration(oldRecord.TTL)*time.Second-time.Second)) {
		t.Fatal("source retention observation incomplete")
	}
	l.Evidence["source_retention_provider_completed_at"] = at
	l.Evidence["source_retention_observations"] = observations
	record()

	migrated := binding(first)
	if migrated.ExternalID == bindings[0].ExternalID || migrated.ConnectionID != runtimeIDs[1] {
		t.Fatal("current release did not move to exact destination")
	}
	var before, after planning.ServiceConfig
	if json.Unmarshal(bindings[0].Config, &before) != nil || json.Unmarshal(migrated.Config, &after) != nil {
		t.Fatal("decode applied release")
	}
	if before.Source != after.Source || before.CommitSHA != after.CommitSHA || !reflect.DeepEqual(before.AppliedInputs, after.AppliedInputs) || !reflect.DeepEqual(before.BuildTarget, after.BuildTarget) {
		t.Fatal("migration changed applied release inputs")
	}
	var currentRoutes struct{ Routes []cloudinfra.Route }
	c.must(http.StatusOK, http.MethodGet, base+"/domain-routes", nil, &currentRoutes)
	if len(currentRoutes.Routes) != 1 || currentRoutes.Routes[0].Binding.InstanceID != migrated.ExternalID {
		t.Fatal("migrated route identity mismatch")
	}
	routes[0] = currentRoutes.Routes[0]
	verifyPublic(0, 1, markers[0])
	l.Evidence["current_release_migration"] = "passed"
	l.Evidence["migrated_binding"] = migrated
	record()
	next := l.RunID + "-" + first + "-release-two"
	c.must(http.StatusOK, http.MethodPatch, "/api/v1/variables/"+variables[0], map[string]string{"value": next}, nil)
	deploy(first, targets[0].Name, "/api")
	future := binding(first)
	if future.ConnectionID != runtimeIDs[1] || future.ExternalID == migrated.ExternalID {
		t.Fatal("future release destination mismatch")
	}
	c.must(http.StatusOK, http.MethodGet, base+"/domain-routes", nil, &currentRoutes)
	if len(currentRoutes.Routes) != 1 || currentRoutes.Routes[0].Input.Hostname != routes[0].Input.Hostname || currentRoutes.Routes[0].Binding.InstanceID != future.ExternalID {
		t.Fatal("future release route mismatch")
	}
	routes[0] = currentRoutes.Routes[0]
	verifyPublic(0, 1, next)
	verifyPublic(1, 0, markers[1])
	verifyPublic(2, 1, markers[2])
	l.Evidence["future_release"] = "passed"
	l.Evidence["future_binding"] = future
	record()
}

func liveDatabase(t *testing.T, path string, l *liveLedger) string {
	t.Helper()
	admin := os.Getenv("LOOMPLANE_TEST_DATABASE_URL")
	endpoint, err := url.Parse(admin)
	if err != nil || admin == "" {
		t.Fatal("isolated database admin URL required")
	}
	db, err := pgx.Connect(t.Context(), admin)
	if err != nil {
		t.Fatal("isolated database admin unavailable")
	}
	defer db.Close(t.Context())
	if err = db.QueryRow(t.Context(), "SELECT system_identifier::text FROM pg_control_system()").Scan(&l.DatabaseCluster); err != nil {
		t.Fatal("isolated database cluster identity unavailable")
	}
	l.Database = "lp_live_" + strings.TrimPrefix(l.RunID, "lp-live-")
	if err = liveWrite(path, l); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(t.Context(), "CREATE DATABASE "+pgx.Identifier{l.Database}.Sanitize()); err != nil {
		t.Fatal("isolated database creation failed; refusing existing name")
	}
	if err = db.QueryRow(t.Context(), "SELECT oid FROM pg_database WHERE datname=$1", l.Database).Scan(&l.DatabaseOID); err != nil {
		t.Fatal("isolated database identity unresolved")
	}
	if err = liveWrite(path, l); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := liveDropDatabase(ctx, l); err != nil {
			t.Error(err)
		}
	})
	endpoint.Path = "/" + l.Database
	return endpoint.String()
}
func liveDropDatabase(ctx context.Context, l *liveLedger) error {
	if l.Database == "" {
		return nil
	}
	if l.Database != "lp_live_"+strings.TrimPrefix(l.RunID, "lp-live-") {
		return fmt.Errorf("isolated database ownership mismatch")
	}
	admin := os.Getenv("LOOMPLANE_TEST_DATABASE_URL")
	if admin == "" {
		return fmt.Errorf("isolated database admin URL required")
	}
	db, err := pgx.Connect(ctx, admin)
	if err != nil {
		return fmt.Errorf("isolated database admin unavailable")
	}
	defer db.Close(ctx)
	var cluster string
	if err = db.QueryRow(ctx, "SELECT system_identifier::text FROM pg_control_system()").Scan(&cluster); err != nil || cluster == "" || cluster != l.DatabaseCluster {
		return fmt.Errorf("isolated database cluster identity changed or is unavailable")
	}
	var oid uint32
	if err = db.QueryRow(ctx, "SELECT oid FROM pg_database WHERE datname=$1", l.Database).Scan(&oid); errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil || l.DatabaseOID == 0 || oid != l.DatabaseOID {
		return fmt.Errorf("isolated database identity unresolved or changed")
	}
	if _, err = db.Exec(ctx, "DROP DATABASE "+pgx.Identifier{l.Database}.Sanitize()+" WITH (FORCE)"); err != nil {
		return fmt.Errorf("isolated database cleanup failed")
	}
	return nil
}
