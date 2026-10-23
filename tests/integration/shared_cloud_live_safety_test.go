//go:build integration

package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/pkg/provider"
)

type liveComputeReader interface {
	ListServers(context.Context, provider.InfrastructureCredential) ([]provider.InfrastructureServer, error)
	GetServer(context.Context, provider.InfrastructureCredential, string) (provider.InfrastructureServer, error)
	DeleteServer(context.Context, provider.InfrastructureCredential, string) error
}
type liveDNSReader interface {
	GetZone(context.Context, provider.InfrastructureCredential, string) (provider.DNSZone, error)
	ListRecords(context.Context, provider.InfrastructureCredential, provider.DNSZone) ([]provider.DNSRecord, error)
	DeleteRecord(context.Context, provider.InfrastructureCredential, provider.DNSDeleteRequest) error
}

// livePatchObserver preserves requests, responses, bodies and errors. The
// provider's successful response headers establish a completion lower bound;
// the product still reads and checkpoints its own returned DNS receipt.
type livePatchObserver struct {
	delegate                   http.RoundTripper
	path, destination, journal string
	mutex                      sync.Mutex
	at                         time.Time
	err                        error
}

func (o *livePatchObserver) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := o.delegate.RoundTrip(request)
	o.mutex.Lock()
	defer o.mutex.Unlock()
	if err == nil && request.URL.Host == "api.vercel.com" && request.URL.Query().Get("teamId") == liveTeam && request.Method == http.MethodPatch && request.URL.Path == o.path && response.StatusCode >= 200 && response.StatusCode < 300 {
		if !o.at.IsZero() {
			o.err = fmt.Errorf("multiple DNS PATCH completions")
		} else {
			o.at = time.Now().UTC()
			o.err = liveWrite(o.journal, map[string]any{"record_path": o.path, "destination": o.destination, "completed_at": o.at})
		}
	}
	return response, err
}
func (o *livePatchObserver) completion() (time.Time, error) {
	o.mutex.Lock()
	defer o.mutex.Unlock()
	return o.at, o.err
}

func liveObserveSource(ctx context.Context, observer *livePatchObserver, probe func(time.Time) ([]combinedSourceObservation, error), ttl time.Duration) ([]combinedSourceObservation, error) {
	var observations []combinedSourceObservation
	for {
		if err := ctx.Err(); err != nil {
			return observations, err
		}
		at, err := observer.completion()
		if err != nil {
			return observations, err
		}
		until := time.Now().Add(time.Second)
		if !at.IsZero() {
			if !time.Now().Before(at.Add(ttl)) {
				return observations, nil
			}
			if at.Add(ttl).Before(until) {
				until = at.Add(ttl)
			}
		}
		samples, err := probe(until)
		observations = append(observations, samples...)
		if err != nil {
			return observations, err
		}
	}
}

type liveTestTransport struct {
	response *http.Response
	err      error
	request  *http.Request
}

func (f *liveTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	f.request = r
	return f.response, f.err
}
func TestSharedCloudLivePatchObservationTransparent(t *testing.T) {
	request, _ := http.NewRequestWithContext(t.Context(), http.MethodPatch, "https://api.vercel.com/v1/domains/records/old?teamId="+liveTeam, nil)
	response := &http.Response{StatusCode: 200, Body: http.NoBody}
	delegate := &liveTestTransport{response: response}
	observer := &livePatchObserver{delegate: delegate, path: request.URL.Path, destination: "203.0.113.1", journal: filepath.Join(t.TempDir(), "patch.json")}
	got, err := observer.RoundTrip(request)
	if got != nil {
		defer got.Body.Close()
	}
	if err != nil || got != response || delegate.request != request {
		t.Fatal("observer changed transport semantics")
	}
	if at, writeErr := observer.completion(); at.IsZero() || writeErr != nil {
		t.Fatal("completion was not recorded")
	}
	sentinel := errors.New("network failure")
	delegate.err = sentinel
	got, err = observer.RoundTrip(request)
	if got != nil {
		defer got.Body.Close()
	}
	if got != response || !errors.Is(err, sentinel) {
		t.Fatal("observer changed error")
	}
}

type liveFakeCompute struct {
	servers    []provider.InfrastructureServer
	failure    error
	getFailure error
	deleted    []string
}

func (f *liveFakeCompute) ListServers(context.Context, provider.InfrastructureCredential) ([]provider.InfrastructureServer, error) {
	return f.servers, f.failure
}
func (f *liveFakeCompute) GetServer(context.Context, provider.InfrastructureCredential, string) (provider.InfrastructureServer, error) {
	if f.getFailure != nil {
		return provider.InfrastructureServer{}, f.getFailure
	}
	if f.failure != nil {
		return provider.InfrastructureServer{}, f.failure
	}
	return provider.InfrastructureServer{}, provider.ErrInfrastructureNotFound
}
func (f *liveFakeCompute) DeleteServer(_ context.Context, _ provider.InfrastructureCredential, id string) error {
	f.deleted = append(f.deleted, id)
	return f.failure
}

type liveFakeDNS struct {
	zone    provider.DNSZone
	records []provider.DNSRecord
	failure error
	deleted []string
}

func (f *liveFakeDNS) GetZone(context.Context, provider.InfrastructureCredential, string) (provider.DNSZone, error) {
	return f.zone, f.failure
}
func (f *liveFakeDNS) ListRecords(context.Context, provider.InfrastructureCredential, provider.DNSZone) ([]provider.DNSRecord, error) {
	return f.records, f.failure
}
func (f *liveFakeDNS) DeleteRecord(_ context.Context, _ provider.InfrastructureCredential, request provider.DNSDeleteRequest) error {
	f.deleted = append(f.deleted, request.Expected.ID)
	f.records = slices.DeleteFunc(f.records, func(r provider.DNSRecord) bool { return r.ID == request.Expected.ID })
	return f.failure
}
func liveTestHost(intent liveHostIntent) provider.InfrastructureServer {
	spec := provider.ServerSpec{OperationID: intent.Operation, Name: intent.Name, Region: liveRegion, Size: liveSize, Image: liveImage}
	raw, _ := json.Marshal(spec)
	sum := sha256.Sum256(raw)
	return provider.InfrastructureServer{ID: "42", AccountID: "account", Name: intent.Name, Region: liveRegion, Size: liveSize, Tags: []string{provider.ServerOperationTag(intent.Operation), "loomplane-spec-" + hex.EncodeToString(sum[:])}}
}
func TestSharedCloudLiveCleanupSafety(t *testing.T) {
	for _, scenario := range []string{"partial", "unsubmitted", "lost-response", "foreign-host", "unknown-create", "compute-error", "dns-error", "rotated-id", "dns-drift", "unknown-dns-create", "delete-uncertain", "dns-transition"} {
		t.Run(scenario, func(t *testing.T) {
			l := &liveLedger{RunID: "lp-live-0123456789abcdef", Account: "account", Zone: provider.DNSZone{ID: "zone", Name: liveZone, AccountID: liveTeam, Verified: true}}
			intent := liveHostIntent{Operation: "operation", Name: l.RunID + "-host1", Attempted: true}
			actual := liveTestHost(intent)
			intent.Receipt = &actual
			l.Hosts = []liveHostIntent{intent}
			compute := &liveFakeCompute{servers: []provider.InfrastructureServer{actual}}
			dns := &liveFakeDNS{zone: l.Zone}
			record := provider.DNSRecord{ID: "old", ZoneID: "zone", Name: l.RunID + "-app0", Type: "A", Value: "203.0.113.1", TTL: 60, Owner: "loomplane:route:owned"}
			l.Records = []liveDNSIntent{{Desired: record, Receipt: &record}}
			dns.records = []provider.DNSRecord{record}
			switch scenario {
			case "partial":
				l.Records = nil
				dns.records = nil
			case "unsubmitted":
				l.Hosts[0].Attempted = false
				l.Hosts[0].Receipt = nil
				compute.servers = nil
				l.Records = nil
				dns.records = nil
			case "lost-response":
				l.Hosts[0].Receipt = nil
			case "foreign-host":
				compute.servers[0].AccountID = "foreign"
			case "unknown-create":
				l.Hosts[0].Receipt = nil
				compute.servers = nil
			case "compute-error":
				compute.failure = errors.New("network")
			case "dns-error":
				dns.failure = errors.New("authentication")
			case "rotated-id":
				dns.records[0].ID = "rotated"
			case "unknown-dns-create":
				l.Records[0].Receipt = nil
				l.Records[0].Attempted = true
				dns.records = nil
			case "delete-uncertain":
				compute.getFailure = errors.New("network")
			case "dns-transition":
				destination := record
				destination.Value = "203.0.113.2"
				l.Records[0].Transitions = []provider.DNSRecord{destination}
				dns.records[0] = destination
			case "dns-drift":
				dns.records[0].Owner = "foreign"
			}
			p := &liveProviders{compute: compute, dns: dns}
			err := liveInspectResources(t.Context(), filepath.Join(t.TempDir(), "state.json"), l, p, true)
			wantError := scenario == "foreign-host" || scenario == "unknown-create" || scenario == "compute-error" || scenario == "dns-error" || scenario == "dns-drift" || scenario == "unknown-dns-create" || scenario == "delete-uncertain"
			if (err != nil) != wantError || l.Cleaned == wantError {
				t.Fatalf("cleanup=%v cleaned=%v", err, l.Cleaned)
			}
			if (scenario == "foreign-host" || scenario == "unknown-create" || scenario == "compute-error" || scenario == "unsubmitted") && len(compute.deleted) != 0 {
				t.Fatal("unsafe host deletion")
			}
			if (scenario == "dns-error" || scenario == "dns-drift") && len(compute.deleted) != 1 {
				t.Fatal("safe independent host cleanup skipped")
			}
			if scenario == "rotated-id" && !reflect.DeepEqual(dns.deleted, []string{"rotated"}) {
				t.Fatal("cleanup used stale record ID")
			}
		})
	}
}
