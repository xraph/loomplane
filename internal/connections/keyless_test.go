package connections

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/clouds/aws"
	"github.com/xraph/loomplane/internal/clouds/keyless"
	"github.com/xraph/loomplane/internal/issuer"
	"github.com/xraph/loomplane/internal/security"
)

var _ KeylessIssuer = (*issuer.Service)(nil)

// fakeIssuer makes tickets anyone can predict, so a test can tell one it
// made.
type fakeIssuer struct {
	usable  bool
	reason  string
	tickets int
}

func (*fakeIssuer) URL() string { return "https://loomplane.example" }

func (f *fakeIssuer) Ticket(_ context.Context, subject, audience string) (string, error) {
	f.tickets++
	return fakeTicket(subject, audience), nil
}

func (f *fakeIssuer) Usable() (bool, string) { return f.usable, f.reason }

func fakeTicket(subject, audience string) string { return "ticket-for " + subject + " at " + audience }

// fakeKeylessCloud takes a role_arn and a region, as AWS does.
type fakeKeylessCloud struct{}

func (fakeKeylessCloud) KeylessSettings(in map[string]string) (map[string]string, error) {
	role, region := strings.TrimSpace(in["role_arn"]), strings.TrimSpace(in["region"])
	if role == "" || region == "" {
		return nil, errors.New("give a role_arn and a region")
	}
	return map[string]string{"role_arn": role, "region": region}, nil
}

func (fakeKeylessCloud) KeylessAudience(map[string]string) string { return "sts.amazonaws.com" }

func (fakeKeylessCloud) KeylessSetup(issuer, subject string) []keyless.Step {
	return []keyless.Step{{Title: "Trust Loomplane", Command: "trust " + issuer + " as " + subject}}
}

// fakeKeylessChecker is AWS, roughly: a pasted key is "key:" and its
// account, and a handout's account is its role's. It refuses a handout
// while refuse is set.
type fakeKeylessChecker struct {
	refuse  *bool
	checked *[]byte
}

func (f fakeKeylessChecker) Parse(credential []byte, settings map[string]string) (Check, error) {
	if h, ok, err := keyless.Parse(credential); ok {
		if err != nil {
			return Check{}, fmt.Errorf("%w: %w", ErrInvalid, err)
		}
		return Check{Details: map[string]string{"role_arn": h.Settings["role_arn"], "region": h.Settings["region"]}}, nil
	}
	if !strings.HasPrefix(string(credential), "key:") {
		return Check{}, fmt.Errorf("%w: not a key", ErrInvalid)
	}
	return Check{Details: map[string]string{"region": settings["region"]}}, nil
}

func (f fakeKeylessChecker) Check(_ context.Context, credential []byte, settings map[string]string) (Check, error) {
	c, err := f.Parse(credential, settings)
	if err != nil {
		return Check{}, err
	}
	if f.checked != nil {
		*f.checked = append([]byte(nil), credential...)
	}
	if h, ok, _ := keyless.Parse(credential); ok {
		if f.refuse != nil && *f.refuse {
			return Check{}, fmt.Errorf("%w: not authorized to perform sts:AssumeRoleWithWebIdentity", aws.ErrRejected)
		}
		c.Account = strings.Split(h.Settings["role_arn"], ":")[4]
		return c, nil
	}
	c.Account = strings.TrimPrefix(string(credential), "key:")
	return c, nil
}

// reservationStore keeps reservations in memory.
type reservationStore struct {
	AppStore
	held map[string]*Reservation
}

func (r *reservationStore) PutReservation(_ context.Context, v *Reservation) error {
	if _, taken := r.held[v.ID]; taken {
		return errors.New("reservation exists")
	}
	kept := *v
	r.held[v.ID] = &kept
	return nil
}

func (r *reservationStore) TakeReservation(_ context.Context, id string) (*Reservation, error) {
	v, ok := r.held[id]
	if !ok {
		return nil, ErrNotFound
	}
	delete(r.held, id)
	return v, nil
}

func (r *reservationStore) SweepReservations(_ context.Context, before time.Time) (int64, error) {
	var n int64
	for id, v := range r.held {
		if v.ExpiresAt.Before(before) {
			delete(r.held, id)
			n++
		}
	}
	return n, nil
}

// countingSecrets counts reads of Vault.
type countingSecrets struct {
	*memSecrets
	reveals int
}

func (c *countingSecrets) Reveal(ctx context.Context, org, id string) ([]byte, error) {
	c.reveals++
	return c.memSecrets.Reveal(ctx, org, id)
}

type keylessRig struct {
	s        *Service
	store    *memStore
	secrets  *countingSecrets
	held     *reservationStore
	issuer   *fakeIssuer
	audit    *connAudit
	clock    time.Time
	refuse   bool
	checked  []byte
	warnings []string
}

const (
	roleArn      = "arn:aws:iam::111111111111:role/loomplane"
	otherRoleArn = "arn:aws:iam::222222222222:role/loomplane"
)

var roleSettings = map[string]string{"role_arn": roleArn, "region": "us-east-1"}

// newKeylessRig is a Service with a fake keyless cloud registered as aws,
// and the same one as gcp.
func newKeylessRig(t *testing.T) *keylessRig {
	t.Helper()
	r := &keylessRig{
		store:   &memStore{rows: map[string]Connection{}},
		secrets: &countingSecrets{memSecrets: &memSecrets{kept: map[string][]byte{}}},
		held:    &reservationStore{held: map[string]*Reservation{}},
		issuer:  &fakeIssuer{usable: true},
		audit:   &connAudit{},
		clock:   time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC),
	}
	checker := fakeKeylessChecker{refuse: &r.refuse, checked: &r.checked}
	r.s = NewService(r.store, r.secrets, map[string]Checker{AWS: checker, GCP: checker, Vercel: checker},
		inventoryAuthorization{}, r.audit, func(msg string, err error) { r.warnings = append(r.warnings, msg+": "+err.Error()) })
	r.s.now = func() time.Time { return r.clock }
	r.s.Apps = r.held
	r.s.Keyless = r.issuer
	r.s.KeylessClouds = map[string]KeylessCloud{AWS: fakeKeylessCloud{}, GCP: fakeKeylessCloud{}}
	return r
}

func (r *keylessRig) prepare(t *testing.T, p security.Principal) string {
	t.Helper()
	setup, err := r.s.PrepareKeyless(t.Context(), p, AWS)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	return setup.ConnectionID
}

func (r *keylessRig) connect(t *testing.T) *Connection {
	t.Helper()
	c, err := r.s.CreateKeyless(t.Context(), starter, KeylessInput{ConnectionID: r.prepare(t, starter), Name: "Prod", Settings: roleSettings})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return c
}

func TestPrepareKeylessReservesAnID(t *testing.T) {
	r := newKeylessRig(t)
	setup, err := r.s.PrepareKeyless(t.Context(), starter, AWS)
	if err != nil {
		t.Fatal(err)
	}
	id := setup.ConnectionID
	if !strings.HasPrefix(id, "conn_") {
		t.Fatalf("id: %q", id)
	}
	if setup.Subject != keyless.Subject("org", id) || setup.Issuer != "https://loomplane.example" || setup.Audience != "sts.amazonaws.com" {
		t.Fatalf("setup: %+v", setup)
	}
	if !reflect.DeepEqual(setup.Steps, fakeKeylessCloud{}.KeylessSetup(setup.Issuer, setup.Subject)) {
		t.Fatalf("steps: %+v", setup.Steps)
	}
	expires := r.clock.Add(24 * time.Hour)
	if setup.ExpiresAt == nil || !setup.ExpiresAt.Equal(expires) {
		t.Fatalf("expires: %v", setup.ExpiresAt)
	}
	held := r.held.held[id]
	if held == nil || held.OrgID != "org" || held.UserID != "ada" || held.Cloud != AWS || !held.ExpiresAt.Equal(expires) || !held.CreatedAt.Equal(r.clock) {
		t.Fatalf("reservation: %+v", held)
	}
	if again := r.prepare(t, starter); again == id {
		t.Fatal("two setups share an ID")
	}
	if r.issuer.tickets != 0 {
		t.Fatal("a setup carries no ticket, so it makes none")
	}

	// Google's audience names a provider you make during the setup.
	g, err := r.s.PrepareKeyless(t.Context(), starter, GCP)
	if err != nil || g.Audience != "" {
		t.Fatalf("gcp: %+v %v", g, err)
	}
	if _, err := r.s.PrepareKeyless(t.Context(), starter, Vercel); !errors.Is(err, ErrInvalid) {
		t.Fatalf("vercel: %v", err)
	}
	r.s.authz = inventoryAuthorization{denied: security.CloudAccountManage}
	if _, err := r.s.PrepareKeyless(t.Context(), starter, AWS); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("forbidden: %v", err)
	}
}

func TestKeylessSetupForUsesTheConnectionsOwnID(t *testing.T) {
	r := newKeylessRig(t)
	r.store.rows["conn_1"] = Connection{ID: "conn_1", OrgID: "org", Cloud: AWS, Method: MethodToken}
	setup, err := r.s.KeylessSetupFor(t.Context(), starter, "conn_1")
	if err != nil {
		t.Fatal(err)
	}
	if setup.ConnectionID != "conn_1" || setup.Subject != keyless.Subject("org", "conn_1") || setup.Audience != "sts.amazonaws.com" || setup.ExpiresAt != nil || len(setup.Steps) != 1 {
		t.Fatalf("setup: %+v", setup)
	}
	if len(r.held.held) != 0 {
		t.Fatalf("reserved: %v", r.held.held)
	}
	r.store.rows["conn_far"] = Connection{ID: "conn_far", OrgID: "elsewhere", Cloud: AWS}
	if _, err := r.s.KeylessSetupFor(t.Context(), starter, "conn_far"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other org: %v", err)
	}
}

func TestCreateKeylessTakesItsReservationOnce(t *testing.T) {
	r := newKeylessRig(t)
	id := r.prepare(t, starter)
	in := KeylessInput{ConnectionID: id, Name: "Prod", Settings: roleSettings}

	// Someone in another organization can't use it, and doesn't use it up.
	if _, err := r.s.CreateKeyless(t.Context(), security.Principal{OrgID: "elsewhere", UserID: "eve"}, in); !errors.Is(err, ErrInvalid) {
		t.Fatalf("other org: %v", err)
	}
	if _, err := r.s.CreateKeyless(t.Context(), starter, in); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := r.s.CreateKeyless(t.Context(), starter, in); !errors.Is(err, ErrInvalid) {
		t.Fatalf("second create: %v", err)
	}

	expired := r.prepare(t, starter)
	r.clock = r.clock.Add(24*time.Hour + time.Second)
	if _, err := r.s.CreateKeyless(t.Context(), starter, KeylessInput{ConnectionID: expired, Name: "Late", Settings: roleSettings}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expired: %v", err)
	}
	if _, kept := r.held.held[expired]; kept {
		t.Fatal("an expired reservation was put back")
	}

	// A reservation for a cloud Loomplane doesn't connect keylessly.
	r.held.held["conn_vercel"] = &Reservation{ID: "conn_vercel", OrgID: "org", UserID: "ada", Cloud: Vercel, ExpiresAt: r.clock.Add(time.Hour), CreatedAt: r.clock}
	if _, err := r.s.CreateKeyless(t.Context(), starter, KeylessInput{ConnectionID: "conn_vercel", Name: "Other", Settings: roleSettings}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("other cloud: %v", err)
	}
	if _, err := r.s.CreateKeyless(t.Context(), starter, KeylessInput{ConnectionID: "conn_unknown", Name: "Other", Settings: roleSettings}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown: %v", err)
	}
	if len(r.store.rows) != 1 {
		t.Fatalf("rows: %v", r.store.rows)
	}
}

// A trust the cloud refuses, such as one you haven't finished setting up,
// leaves the reservation for another try.
func TestCreateKeylessKeepsTheReservationWhenTheCheckFails(t *testing.T) {
	r := newKeylessRig(t)
	id := r.prepare(t, starter)
	r.refuse = true
	_, err := r.s.CreateKeyless(t.Context(), starter, KeylessInput{ConnectionID: id, Name: "Prod", Settings: roleSettings})
	if !errors.Is(err, ErrInvalid) || strings.Contains(err.Error(), "ticket-for") {
		t.Fatalf("refused: %v", err)
	}
	if _, err = r.s.CreateKeyless(t.Context(), starter, KeylessInput{ConnectionID: id, Name: "Prod", Settings: map[string]string{"region": "us-east-1"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no role: %v", err)
	}
	if r.held.held[id] == nil {
		t.Fatal("the reservation was used up by a failed create")
	}
	r.refuse = false
	c, err := r.s.CreateKeyless(t.Context(), starter, KeylessInput{ConnectionID: id, Name: "Prod", Settings: roleSettings})
	if err != nil || c.ID != id {
		t.Fatalf("retry: %+v %v", c, err)
	}
}

func TestCreateKeylessStoresNothingInVault(t *testing.T) {
	r := newKeylessRig(t)
	id := r.prepare(t, starter)
	c, err := r.s.CreateKeyless(t.Context(), starter, KeylessInput{ConnectionID: id, Name: " Prod ", Settings: roleSettings})
	if err != nil {
		t.Fatal(err)
	}
	if c.ID != id || c.Method != MethodKeyless || c.Name != "Prod" || c.Cloud != AWS || c.Account != "111111111111" || c.Status != StatusReady || c.SignedInBy != "" || c.CreatedBy != "ada" {
		t.Fatalf("connection: %+v", c)
	}
	if r.secrets.puts != 0 || len(r.secrets.kept) != 0 {
		t.Fatalf("vault: %d puts, %v", r.secrets.puts, r.secrets.kept)
	}
	if stored := r.store.rows[id]; stored.Method != MethodKeyless {
		t.Fatalf("stored: %+v", stored)
	}
	if len(r.audit.actions) != 1 || r.audit.actions[0] != "cloud_connection.create" {
		t.Fatalf("audit: %v", r.audit.actions)
	}
	h, ok, err := keyless.Parse(r.checked)
	if !ok || err != nil || h.Subject != keyless.Subject("org", id) || h.Ticket != fakeTicket(h.Subject, "sts.amazonaws.com") {
		t.Fatalf("checked: %+v %v", h, err)
	}
	out, err := json.Marshal(c)
	if err != nil || strings.Contains(string(out), "ticket-for") {
		t.Fatalf("response: %s %v", out, err)
	}
}

func TestCredentialHandsOutAKeylessHandout(t *testing.T) {
	r := newKeylessRig(t)
	c := r.connect(t)
	got, cred, err := r.s.Credential(t.Context(), "org", c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != c.ID {
		t.Fatalf("connection: %+v", got)
	}
	h, ok, err := keyless.Parse(cred)
	if !ok || err != nil {
		t.Fatalf("parse: %v %v", ok, err)
	}
	want := keyless.Handout{Type: keyless.Type, Subject: keyless.Subject("org", c.ID), Audience: "sts.amazonaws.com", Settings: c.Details}
	want.Ticket = fakeTicket(want.Subject, want.Audience)
	if !reflect.DeepEqual(h, want) {
		t.Fatalf("handout:\n got %+v\nwant %+v", h, want)
	}
	if r.secrets.reveals != 0 {
		t.Fatalf("read Vault %d times", r.secrets.reveals)
	}
}

func TestRecheckFailsARefusedKeylessTrust(t *testing.T) {
	r := newKeylessRig(t)
	c := r.connect(t)
	lapsed := 0
	r.s.Lapsed = func(context.Context, *Connection) { lapsed++ }
	r.refuse = true
	got, err := r.s.Recheck(t.Context(), starter, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusFailed || !strings.Contains(got.Message, "AssumeRoleWithWebIdentity") || got.Generation != c.Generation || lapsed != 0 {
		t.Fatalf("recheck: %+v, %d lapsed", got, lapsed)
	}
	if r.secrets.reveals != 0 {
		t.Fatalf("read Vault %d times", r.secrets.reveals)
	}
	r.refuse = false
	if got, err = r.s.Recheck(t.Context(), starter, c.ID); err != nil || got.Status != StatusReady {
		t.Fatalf("recheck again: %+v %v", got, err)
	}
}

// tokenConnection is an AWS connection made with a pasted key in account
// 111111111111.
func (r *keylessRig) tokenConnection(t *testing.T) *Connection {
	t.Helper()
	c, err := r.s.Create(t.Context(), starter, CreateInput{Cloud: AWS, Name: "Prod", Credential: "key:111111111111", Settings: map[string]string{"region": "us-east-1"}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return c
}

func TestReplaceKeylessKeepsTheIDAndDropsTheSecret(t *testing.T) {
	r := newKeylessRig(t)
	c := r.tokenConnection(t)
	if len(r.secrets.kept) != 1 {
		t.Fatalf("vault: %v", r.secrets.kept)
	}
	got, err := r.s.ReplaceKeyless(t.Context(), starter, c.ID, roleSettings)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != c.ID || got.Method != MethodKeyless || got.Generation != c.Generation+1 || got.Details["role_arn"] != roleArn || got.Status != StatusReady {
		t.Fatalf("replaced: %+v", got)
	}
	if len(r.secrets.kept) != 0 {
		t.Fatalf("the secret is still in Vault: %v", r.secrets.kept)
	}
	if r.audit.actions[len(r.audit.actions)-1] != "cloud_connection.replace" {
		t.Fatalf("audit: %v", r.audit.actions)
	}
	h, _, _ := keyless.Parse(r.checked)
	if h.Subject != keyless.Subject("org", c.ID) {
		t.Fatalf("checked: %+v", h)
	}
	_, cred, err := r.s.Credential(t.Context(), "org", c.ID)
	if _, ok, _ := keyless.Parse(cred); err != nil || !ok {
		t.Fatalf("credential: %v", err)
	}

	// And back to a key, through Replace as it is.
	back, err := r.s.Replace(t.Context(), starter, c.ID, ReplaceInput{Credential: "key:111111111111", Settings: map[string]string{"region": "us-east-1"}})
	if err != nil || back.Method != MethodToken || string(r.secrets.kept["org/"+c.ID]) != "key:111111111111" {
		t.Fatalf("back to a key: %+v %v", back, err)
	}
}

func TestReplaceKeylessRefusesAnotherAccount(t *testing.T) {
	r := newKeylessRig(t)
	c := r.tokenConnection(t)
	other := maps.Clone(roleSettings)
	other["role_arn"] = otherRoleArn
	if _, err := r.s.ReplaceKeyless(t.Context(), starter, c.ID, other); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "the verified account changed") {
		t.Fatalf("other account: %v", err)
	}
	stored := r.store.rows[c.ID]
	if stored.Method != MethodToken || stored.Generation != c.Generation || len(r.secrets.kept) != 1 {
		t.Fatalf("changed: %+v, vault %v", stored, r.secrets.kept)
	}
}

func TestPastedKeylessJSONIsRefused(t *testing.T) {
	pasted, err := json.Marshal(keyless.Handout{Type: keyless.Type, Subject: "org:org:connection:conn_1", Audience: "sts.amazonaws.com", Ticket: "secret-ticket", Settings: roleSettings})
	if err != nil {
		t.Fatal(err)
	}
	// One that isn't usable is still a handout.
	broken := []byte(`{"type":"loomplane_keyless"}`)
	for name, c := range map[string]Checker{
		"gcp": GCPChecker{}, "vercel": VercelChecker{}, "aws": AWSChecker{}, "render": RenderChecker{},
		"digitalocean": DigitalOceanChecker{}, "azure": AzureChecker{}, "server": ServerChecker{},
		"kubernetes": KubernetesChecker{}, "registry": RegistryChecker{}, "plugin": PluginChecker{},
	} {
		for _, cred := range [][]byte{pasted, broken} {
			err = refusePastedGrant(c, cred, MethodToken)
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "connect keylessly from the connect dialog instead") {
				t.Errorf("%s: %v", name, err)
			}
		}
		if g, ok := c.(GrantChecker); !ok || g.IsGrant([]byte("key:111111111111")) {
			t.Errorf("%s: a pasted token is a grant, or IsGrant is missing", name)
		}
	}

	r := newKeylessRig(t)
	r.s.checkers[AWS] = AWSChecker{}
	_, err = r.s.Create(t.Context(), starter, CreateInput{Cloud: AWS, Name: "Prod", Credential: string(pasted), Settings: roleSettings})
	if !errors.Is(err, ErrInvalid) || strings.Contains(err.Error(), "secret-ticket") || r.secrets.puts != 0 {
		t.Fatalf("create: %v", err)
	}
}

func TestKeylessIsUnavailableWithoutAUsableIssuer(t *testing.T) {
	r := newKeylessRig(t)
	c := r.connect(t)
	r.store.rows["conn_token"] = Connection{ID: "conn_token", OrgID: "org", Cloud: AWS, Method: MethodToken, Account: "111111111111", Details: map[string]string{"region": "us-east-1"}}
	id := r.prepare(t, starter)
	calls := map[string]func() error{
		"prepare": func() error { _, err := r.s.PrepareKeyless(t.Context(), starter, AWS); return err },
		"setup":   func() error { _, err := r.s.KeylessSetupFor(t.Context(), starter, "conn_token"); return err },
		"create": func() error {
			_, err := r.s.CreateKeyless(t.Context(), starter, KeylessInput{ConnectionID: id, Name: "Next", Settings: roleSettings})
			return err
		},
		"replace": func() error {
			_, err := r.s.ReplaceKeyless(t.Context(), starter, "conn_token", roleSettings)
			return err
		},
		"credential": func() error { _, _, err := r.s.Credential(t.Context(), "org", c.ID); return err },
		"recheck":    func() error { _, err := r.s.Recheck(t.Context(), starter, c.ID); return err },
	}
	r.issuer.usable, r.issuer.reason = false, "public_url isn't HTTPS"
	for name, call := range calls {
		if err := call(); !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "public_url isn't HTTPS") {
			t.Errorf("%s: %v", name, err)
		}
	}
	r.s.Keyless = nil
	for name, call := range calls {
		if err := call(); !errors.Is(err, ErrUnavailable) {
			t.Errorf("%s without an issuer: %v", name, err)
		}
	}
	if r.held.held[id] == nil || r.store.rows[c.ID].Status != StatusReady || r.store.rows["conn_token"].Method != MethodToken {
		t.Fatal("an unavailable issuer changed something")
	}
}
