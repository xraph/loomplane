package dbgateway

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xraph/chronicle"
	"github.com/xraph/chronicle/audit"
	"github.com/xraph/chronicle/store"
	"github.com/xraph/chronicle/store/memory"
	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/dbgateway"
	"github.com/xraph/loomplane/internal/dbgateway/plane"
	"github.com/xraph/loomplane/internal/security"
)

// memChronicle is a Chronicle over an in-memory store.
func memChronicle(t *testing.T) (*chronicle.Chronicle, *memory.Store) {
	t.Helper()
	mem := memory.New()
	c, err := chronicle.New(chronicle.WithStore(store.NewAdapter(mem)))
	if err != nil {
		t.Fatal(err)
	}
	return c, mem
}

// auditedRouteChanges lists the route changes in mem, oldest first, as
// "action user project environment gateway route resource state vN", N
// being the snapshot version.
func auditedRouteChanges(t *testing.T, mem *memory.Store) []string {
	t.Helper()
	res, err := mem.Query(t.Context(), &audit.Query{AppID: "app_1", Order: "asc"})
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(res.Events))
	for _, e := range res.Events {
		if e.Resource != "db_gateway_route" {
			continue
		}
		out = append(out, strings.Join([]string{
			e.Action, e.UserID, fmt.Sprint(e.Metadata["project_id"]), fmt.Sprint(e.Metadata["environment_id"]),
			fmt.Sprint(e.Metadata["gateway_id"]), e.ResourceID, fmt.Sprint(e.Metadata["resource"]), fmt.Sprint(e.Metadata["state"]),
			"v" + fmt.Sprint(e.Metadata["snapshot_version"]),
		}, " "))
	}
	return out
}

// The Chronicle auditor records which project, environment and gateway a
// route change belongs to, in the organization's stream, and never a key
// or a salt.
func TestAuditorRecordsTheGatewayAndProject(t *testing.T) {
	trail, mem := memChronicle(t)
	signing, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sealing, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	gw := &dbgateway.Gateway{
		OrgID: "org_1", ID: "dbgw_1", ProjectID: "proj_1", EnvironmentID: "env_1", Name: "main",
		Provider: "cloud-run", Region: "us-central1", Suffix: "us-central1.db.example.com",
		SigningPublicKey: signing, SealingPublicKey: sealing.PublicKey(),
	}
	salt := []byte("0123456789abcdef")
	route := &dbgateway.RouteRecord{ID: "dbgr_1", Resource: "orders", State: "paused", Salt: salt}
	a := auditor{chronicle: trail, appID: "app_1", log: forge.NewNoopLogger()}
	a.Record(t.Context(), dbgateway.AuditRoutePause, security.Principal{OrgID: "org_1", UserID: "usr_admin"}, gw, route, 7)

	want := []string{"dbGateway.route.pause usr_admin proj_1 env_1 dbgw_1 dbgr_1 orders paused v7"}
	if got := auditedRouteChanges(t, mem); !slices.Equal(got, want) {
		t.Fatalf("audited %v, want %v", got, want)
	}
	res, err := mem.Query(t.Context(), &audit.Query{AppID: "app_1"})
	if err != nil {
		t.Fatal(err)
	}
	e := res.Events[0]
	if e.TenantID != "org_1" || e.Category != "database_gateways" || e.Outcome != "success" {
		t.Fatalf("event %+v", e)
	}
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{
		"salt", "key", "suffix", string(salt), base64.StdEncoding.EncodeToString(salt),
		base64.StdEncoding.EncodeToString(signing), base64.StdEncoding.EncodeToString(sealing.PublicKey().Bytes()),
	} {
		if strings.Contains(strings.ToLower(string(raw)), strings.ToLower(secret)) {
			t.Fatalf("the event carries %q: %s", secret, raw)
		}
	}
}

// recordingLogger keeps what was logged at warn and error level.
type recordingLogger struct {
	forge.Logger
	lines []string
}

func (l *recordingLogger) Warn(msg string, _ ...forge.Field) { l.lines = append(l.lines, "warn: "+msg) }
func (l *recordingLogger) Error(msg string, _ ...forge.Field) {
	l.lines = append(l.lines, "error: "+msg)
}

// Loomplane starts with no key it can sign with and says so loudly, and
// warns a month before the last key expires. A key that takes over later
// puts the warning off.
func TestWarnAboutKeyWindow(t *testing.T) {
	now := time.Now()
	window := func(from, until time.Duration) dbgateway.SigningKey {
		return dbgateway.SigningKey{Trust: plane.TrustKey{NotBefore: now.Add(from), NotAfter: now.Add(until)}}
	}
	month := 30 * 24 * time.Hour
	for _, tc := range []struct {
		name  string
		ready error
		keys  []dbgateway.SigningKey
		want  string
	}{
		{"expired", dbgateway.ErrSigningKeyExpired, []dbgateway.SigningKey{window(-2*time.Hour, -time.Hour)}, "error: every database gateway signing key in dbgateway.signing_key_file has expired"},
		{"not yet valid", dbgateway.ErrSigningKeyNotYetValid, []dbgateway.SigningKey{window(48*time.Hour, month)}, "error: no database gateway signing key in dbgateway.signing_key_file is valid yet"},
		{"expires within 30 days", nil, []dbgateway.SigningKey{window(-time.Hour, month-24*time.Hour)}, "warn: the last database gateway signing key in dbgateway.signing_key_file expires within 30 days"},
		{"a long way off", nil, []dbgateway.SigningKey{window(-time.Hour, month+24*time.Hour)}, ""},
		{"replaced in time", nil, []dbgateway.SigningKey{window(-time.Hour, 24*time.Hour), window(12*time.Hour, 3*month)}, ""},
	} {
		log := &recordingLogger{Logger: forge.NewNoopLogger()}
		warnAboutKeyWindow(log, tc.ready, tc.keys, now)
		switch {
		case tc.want == "" && len(log.lines) != 0:
			t.Errorf("%s: logged %q", tc.name, log.lines)
		case tc.want != "" && (len(log.lines) != 1 || !strings.HasPrefix(log.lines[0], tc.want)):
			t.Errorf("%s: logged %q, want %q", tc.name, log.lines, tc.want)
		}
	}
}
