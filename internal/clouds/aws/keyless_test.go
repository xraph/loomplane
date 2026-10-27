package aws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/clouds/keyless"
)

const (
	testRole    = "arn:aws:iam::111111111111:role/loomplane"
	testTicket  = "ticket-only-the-issuer-makes"
	testSubject = "org:org_1:connection:conn_1"
)

// fakeSTS answers AssumeRoleWithWebIdentity and GetCallerIdentity in the
// query protocol, and remembers what each assume was sent.
type fakeSTS struct {
	srv     *httptest.Server
	mu      sync.Mutex
	assumed []url.Values
	signers []string
	expires func() time.Time
	refuse  bool
	// fail, when set, is what every assume answers instead.
	fail *stsFailure
}

// stsFailure is an error STS answers an assume with.
type stsFailure struct {
	status        int
	code, message string
}

func newFakeSTS(t *testing.T) *fakeSTS {
	t.Helper()
	f := &fakeSTS{expires: func() time.Time { return time.Now().Add(time.Hour) }}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeSTS) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	form, _ := url.ParseQuery(string(body))
	w.Header().Set("Content-Type", "text/xml")
	f.mu.Lock()
	defer f.mu.Unlock()
	switch form.Get("Action") {
	case "AssumeRoleWithWebIdentity":
		f.assumed = append(f.assumed, form)
		if f.fail != nil {
			w.WriteHeader(f.fail.status)
			_, _ = fmt.Fprintf(w, `<ErrorResponse><Error><Type>Sender</Type><Code>%s</Code><Message>%s</Message></Error><RequestId>1</RequestId></ErrorResponse>`, f.fail.code, f.fail.message)
			return
		}
		if f.refuse {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`<ErrorResponse><Error><Type>Sender</Type><Code>AccessDenied</Code><Message>Not authorized to perform sts:AssumeRoleWithWebIdentity</Message></Error><RequestId>1</RequestId></ErrorResponse>`))
			return
		}
		n := len(f.assumed)
		_, _ = fmt.Fprintf(w, `<AssumeRoleWithWebIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><AssumeRoleWithWebIdentityResult>`+
			`<Credentials><AccessKeyId>ASIAKEYLESS%04d</AccessKeyId><SecretAccessKey>temporary-secret-%d</SecretAccessKey><SessionToken>session-%d</SessionToken><Expiration>%s</Expiration></Credentials>`+
			`<AssumedRoleUser><Arn>arn:aws:sts::111111111111:assumed-role/loomplane/%s</Arn><AssumedRoleId>AROAEXAMPLE:%s</AssumedRoleId></AssumedRoleUser>`+
			`</AssumeRoleWithWebIdentityResult><ResponseMetadata><RequestId>1</RequestId></ResponseMetadata></AssumeRoleWithWebIdentityResponse>`,
			n, n, n, f.expires().UTC().Format(time.RFC3339), form.Get("RoleSessionName"), form.Get("RoleSessionName"))
	case "GetCallerIdentity":
		f.signers = append(f.signers, r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`<GetCallerIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><GetCallerIdentityResult>` +
			`<Arn>arn:aws:sts::111111111111:assumed-role/loomplane/loomplane-conn_1</Arn><UserId>AROAEXAMPLE:loomplane-conn_1</UserId><Account>111111111111</Account>` +
			`</GetCallerIdentityResult><ResponseMetadata><RequestId>1</RequestId></ResponseMetadata></GetCallerIdentityResponse>`))
	default:
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`<ErrorResponse><Error><Code>InvalidAction</Code><Message>unknown</Message></Error></ErrorResponse>`))
	}
}

// countingMinter mints token-1, token-2 and so on, for a handout with the
// test's ticket only.
type countingMinter struct {
	mu       sync.Mutex
	minted   int
	deadline bool
}

func (m *countingMinter) Mint(ctx context.Context, h keyless.Handout) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if h.Ticket != testTicket || h.Subject != testSubject || h.Audience != "sts.amazonaws.com" {
		return "", errors.New("not a handout this test made")
	}
	_, m.deadline = ctx.Deadline()
	m.minted++
	return fmt.Sprintf("token-%d", m.minted), nil
}

func testHandout(t *testing.T, settings map[string]string) []byte {
	t.Helper()
	out, err := json.Marshal(keyless.Handout{Type: keyless.Type, Subject: testSubject, Audience: "sts.amazonaws.com", Ticket: testTicket, Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func testKeylessKey(t *testing.T) *Key {
	t.Helper()
	key, err := ParseKey(testHandout(t, map[string]string{"role_arn": testRole, "region": "us-east-1"}))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return key
}

func TestCheckRoleARN(t *testing.T) {
	for _, ok := range []string{testRole, "arn:aws:iam::123456789012:role/teams/platform/Loomplane+Deploy=1,a.b@c-d"} {
		if err := CheckRoleARN(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"", "loomplane", "arn:aws:iam::11111111111:role/loomplane", "arn:aws:iam::111111111111:user/loomplane",
		"arn:aws:iam::111111111111:role/", "arn:aws:sts::111111111111:assumed-role/loomplane/x",
		"arn:aws:iam::111111111111:role/has space", " " + testRole, "arn:aws:iam::111111111111:role/" + strings.Repeat("a", 513),
	} {
		if err := CheckRoleARN(bad); err == nil {
			t.Errorf("%q passed", bad)
		}
	}
}

func TestParseKeyReadsAKeylessHandout(t *testing.T) {
	key := testKeylessKey(t)
	if !key.Keyless() || key.RoleARN != testRole || key.AccessKeyID != "" || key.SecretAccessKey != "" {
		t.Fatalf("key: keyless %v, role %q", key.Keyless(), key.RoleARN)
	}
	plain, err := ParseKey([]byte(`{"access_key_id":"AKIAABCDEFGHIJKLMNOP","secret_access_key":"0123456789abcdefghijklmnopqrstuvwxyz0123"}`))
	if err != nil || plain.Keyless() || plain.RoleARN != "" {
		t.Fatalf("access key: keyless %v, %v", plain != nil && plain.Keyless(), err)
	}
	for name, settings := range map[string]map[string]string{
		"no role":     {"region": "us-east-1"},
		"bad role":    {"role_arn": "arn:aws:iam::111111111111:user/loomplane", "region": "us-east-1"},
		"no region":   {"role_arn": testRole},
		"bad region":  {"role_arn": testRole, "region": "mars"},
		"no settings": nil,
	} {
		_, err := ParseKey(testHandout(t, settings))
		if !errors.Is(err, ErrInvalidKey) || strings.Contains(err.Error(), testTicket) {
			t.Errorf("%s: %v", name, errors.Is(err, ErrInvalidKey))
		}
	}
	broken := []byte(`{"type":"loomplane_keyless","subject":"s","audience":"a","ticket":"` + testTicket + `","settings":"nope"}`)
	if _, err := ParseKey(broken); !errors.Is(err, ErrInvalidKey) || strings.Contains(err.Error(), testTicket) {
		t.Errorf("broken handout: %v", errors.Is(err, ErrInvalidKey))
	}
}

func TestWebIdentityAssumesTheRoleWithAMintedToken(t *testing.T) {
	sts := newFakeSTS(t)
	minter := &countingMinter{}
	base := New(sts.srv.URL)
	c := base.WithMinter(minter)
	who, err := c.Identify(t.Context(), testKeylessKey(t), "us-east-1")
	if err != nil {
		t.Fatalf("identify: %v", err)
	}
	if who.Account != "111111111111" || !strings.Contains(who.ARN, ":assumed-role/loomplane/") {
		t.Fatalf("identity: %+v", who)
	}
	if len(sts.assumed) != 1 || minter.minted != 1 || !minter.deadline {
		t.Fatalf("assumed %d times, minted %d, mint had a deadline: %v", len(sts.assumed), minter.minted, minter.deadline)
	}
	form := sts.assumed[0]
	if form.Get("RoleArn") != testRole || form.Get("WebIdentityToken") != "token-1" || form.Get("RoleSessionName") != "loomplane-conn_1" {
		t.Fatalf("assume: role %q, session %q, token sent: %v", form.Get("RoleArn"), form.Get("RoleSessionName"), form.Get("WebIdentityToken") == "token-1")
	}
	if len(sts.signers) != 1 || !strings.Contains(sts.signers[0], "Credential=ASIAKEYLESS0001/") {
		t.Fatal("GetCallerIdentity wasn't signed with the assumed role's credentials")
	}

	// The client WithMinter copied still has none.
	_, err = base.Identify(t.Context(), testKeylessKey(t), "us-east-1")
	if !errors.Is(err, keyless.ErrNoMinter) || len(sts.assumed) != 1 {
		t.Fatalf("no minter: %v, assumed %d times", err, len(sts.assumed))
	}
}

func TestWebIdentityRenewsWithAFreshToken(t *testing.T) {
	sts := newFakeSTS(t)
	sts.expires = func() time.Time { return time.Now().Add(-time.Second) }
	minter := &countingMinter{}
	cfg := New(sts.srv.URL).WithMinter(minter).Config(testKeylessKey(t), "us-east-1")
	for range 2 {
		if _, err := cfg.Credentials.Retrieve(t.Context()); err != nil {
			t.Fatalf("retrieve: %v", err)
		}
	}
	if minter.minted != 2 || len(sts.assumed) != 2 {
		t.Fatalf("minted %d, assumed %d times", minter.minted, len(sts.assumed))
	}
	if sts.assumed[0].Get("WebIdentityToken") != "token-1" || sts.assumed[1].Get("WebIdentityToken") != "token-2" {
		t.Fatal("the renewal didn't send a fresh token")
	}

	// Credentials that last are kept, and nothing is minted for them.
	sts.expires = func() time.Time { return time.Now().Add(time.Hour) }
	cfg = New(sts.srv.URL).WithMinter(minter).Config(testKeylessKey(t), "us-east-1")
	for range 2 {
		if _, err := cfg.Credentials.Retrieve(t.Context()); err != nil {
			t.Fatalf("retrieve: %v", err)
		}
	}
	if minter.minted != 3 {
		t.Fatalf("minted %d", minter.minted)
	}
}

func TestKeylessRefusedTrustIsRejected(t *testing.T) {
	sts := newFakeSTS(t)
	sts.refuse = true
	_, err := New(sts.srv.URL).WithMinter(&countingMinter{}).Identify(t.Context(), testKeylessKey(t), "us-east-1")
	if !errors.Is(err, ErrRejected) || strings.Contains(err.Error(), "token-1") || strings.Contains(err.Error(), testTicket) {
		t.Fatalf("refused: rejected %v", errors.Is(err, ErrRejected))
	}
}

// failingMinter can't mint, as when Loomplane's issuer can't read its key.
type failingMinter struct{}

func (failingMinter) Mint(context.Context, keyless.Handout) (string, error) {
	return "", errors.New("vault is sealed")
}

// Loomplane or AWS failing isn't the role refusing Loomplane, so none of
// these is ErrRejected, and a connection isn't marked failed over one.
func TestKeylessOutageIsNotARefusal(t *testing.T) {
	for name, fail := range map[string]*stsFailure{
		"sts down":         {http.StatusServiceUnavailable, "ServiceUnavailable", "Service is unavailable"},
		"idp unreachable":  {http.StatusBadRequest, "IDPCommunicationError", "Couldn't connect to the identity provider"},
		"clocks disagree":  {http.StatusBadRequest, "ExpiredTokenException", "Token expired: current date/time 1 must be before the expiration date/time 0"},
		"keys unreachable": {http.StatusBadRequest, "InvalidIdentityToken", "Couldn't retrieve verification key from your identity provider,  please reference AssumeRoleWithWebIdentity documentation for requirements"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel() // STS retries most of these, which takes seconds
			sts := newFakeSTS(t)
			sts.fail = fail
			_, err := New(sts.srv.URL).WithMinter(&countingMinter{}).Identify(t.Context(), testKeylessKey(t), "us-east-1")
			if err == nil || errors.Is(err, ErrRejected) {
				t.Fatalf("err %v, rejected %v", err, errors.Is(err, ErrRejected))
			}
		})
	}
	sts := newFakeSTS(t)
	for name, c := range map[string]*Client{"mint fails": New(sts.srv.URL).WithMinter(failingMinter{}), "no minter": New(sts.srv.URL)} {
		_, err := c.Identify(t.Context(), testKeylessKey(t), "us-east-1")
		if err == nil || errors.Is(err, ErrRejected) {
			t.Errorf("%s: err %v, rejected %v", name, err, errors.Is(err, ErrRejected))
		}
	}
	if _, err := New(sts.srv.URL).Identify(t.Context(), testKeylessKey(t), "us-east-1"); !errors.Is(err, keyless.ErrNoMinter) {
		t.Fatalf("no minter: %v", err)
	}
}

// The role's trust turning Loomplane's token away is a refusal, and says
// so in the keyless trust's words, not an access key's.
func TestKeylessTrustRefusalsAreRejected(t *testing.T) {
	for name, fail := range map[string]*stsFailure{
		"no provider":     {http.StatusBadRequest, "InvalidIdentityToken", "No OpenIDConnect provider found in your account for https://loomplane.example.com"},
		"wrong audience":  {http.StatusBadRequest, "InvalidIdentityToken", "Incorrect token audience"},
		"wrong issuer":    {http.StatusBadRequest, "InvalidIdentityToken", "Issuer does not match the provider's URL"},
		"subject refused": {http.StatusForbidden, "AccessDenied", "Not authorized to perform sts:AssumeRoleWithWebIdentity"},
		"claim refused":   {http.StatusForbidden, "IDPRejectedClaim", "The claim was rejected"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel() // STS retries most of these, which takes seconds
			sts := newFakeSTS(t)
			sts.fail = fail
			_, err := New(sts.srv.URL).WithMinter(&countingMinter{}).Identify(t.Context(), testKeylessKey(t), "us-east-1")
			if !errors.Is(err, ErrRejected) {
				t.Fatalf("err %v isn't a refusal", err)
			}
			if msg := err.Error(); !strings.HasPrefix(msg, "AWS refused Loomplane's keyless token for this role") || strings.Contains(msg, "access key") {
				t.Fatalf("message %q", msg)
			}
		})
	}
}

func TestKeylessSettingsTakeTheRoleAndRegion(t *testing.T) {
	c := New("")
	in := map[string]string{
		"role_arn": " " + testRole + " ", "region": "us-east-1", "access_key": "…MNOP", "principal": "x",
		"certificate_arn":  "arn:aws:acm:us-east-1:111111111111:certificate/abc",
		"rds_recovery_vpc": "vpc-1", "rds_recovery_subnet_group": "private", "rds_recovery_security_group": "sg-copy", "rds_recovery_verifier_group": "sg-verify",
	}
	got, err := c.KeylessSettings(in)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"certificate_arn", "rds_recovery_security_group", "rds_recovery_subnet_group", "rds_recovery_verifier_group", "rds_recovery_vpc", "region", "role_arn"}
	keys := make([]string, 0, len(got))
	for k := range got {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if !slices.Equal(keys, want) || got["role_arn"] != testRole {
		t.Fatalf("settings: %v", got)
	}
	for name, bad := range map[string]map[string]string{
		"no role":        {"region": "us-east-1"},
		"no region":      {"role_arn": testRole},
		"other cert":     {"role_arn": testRole, "region": "us-east-1", "certificate_arn": "arn:aws:acm:eu-west-1:111111111111:certificate/abc"},
		"partial route":  {"role_arn": testRole, "region": "us-east-1", "rds_recovery_vpc": "vpc-1"},
		"user, not role": {"role_arn": "arn:aws:iam::111111111111:user/x", "region": "us-east-1"},
	} {
		if _, err := c.KeylessSettings(bad); err == nil {
			t.Errorf("%s passed", name)
		}
	}
	if c.KeylessAudience(nil) != "sts.amazonaws.com" {
		t.Fatal(c.KeylessAudience(nil))
	}
}

func TestKeylessSetupNamesTheSubject(t *testing.T) {
	steps := New("").KeylessSetup("https://loomplane.example/oidc", testSubject)
	if len(steps) != 3 {
		t.Fatalf("steps: %d", len(steps))
	}
	if steps[0].Command != "aws iam create-open-id-connect-provider --url https://loomplane.example/oidc --client-id-list sts.amazonaws.com" {
		t.Fatalf("provider: %s", steps[0].Command)
	}
	var trust struct {
		Version   string
		Statement []struct {
			Effect    string
			Principal map[string]string
			Action    string
			Condition map[string]map[string]string
		}
	}
	if err := json.Unmarshal([]byte(steps[1].Command), &trust); err != nil {
		t.Fatalf("trust policy: %v\n%s", err, steps[1].Command)
	}
	if trust.Version != "2012-10-17" || len(trust.Statement) != 1 {
		t.Fatalf("trust policy: %+v", trust)
	}
	s := trust.Statement[0]
	want := map[string]string{"loomplane.example/oidc:aud": "sts.amazonaws.com", "loomplane.example/oidc:sub": testSubject}
	if s.Effect != "Allow" || s.Action != "sts:AssumeRoleWithWebIdentity" ||
		s.Principal["Federated"] != "arn:aws:iam::<account-id>:oidc-provider/loomplane.example/oidc" ||
		len(s.Condition) != 1 || fmt.Sprint(s.Condition["StringEquals"]) != fmt.Sprint(want) {
		t.Fatalf("statement: %+v", s)
	}
	var permissions struct {
		Statement []struct {
			Action []string
		}
	}
	if err := json.Unmarshal([]byte(steps[2].Command), &permissions); err != nil || len(permissions.Statement) != 1 || !slices.Equal(permissions.Statement[0].Action, Actions) {
		t.Fatalf("permissions: %v", err)
	}
	for _, step := range steps {
		if step.Title == "" || step.Body == "" || strings.Contains(step.Title+step.Body+step.Command, "\u2014") {
			t.Errorf("step: %+v", step)
		}
	}
	// A trailing slash or plain HTTP, as in development, names the same host.
	if dev := New("").KeylessSetup("http://127.0.0.1:8080/", testSubject); !strings.Contains(dev[1].Command, `"127.0.0.1:8080:sub"`) {
		t.Fatalf("development: %s", dev[1].Command)
	}
}
