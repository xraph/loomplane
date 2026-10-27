package connections

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/clouds/aws"
	"github.com/xraph/loomplane/internal/clouds/keyless"
)

// fakeAWSAccount answers STS and IAM's simulator for account
// 111111111111: an access key is a user there, and a minted token assumes
// the loomplane role.
type fakeAWSAccount struct {
	mu        sync.Mutex
	tokens    []string
	simulated []string
}

func (f *fakeAWSAccount) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	form, _ := url.ParseQuery(string(body))
	keyless := strings.Contains(r.Header.Get("Authorization"), "Credential=ASIAKEYLESSEXAMPLE/")
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "text/xml")
	switch form.Get("Action") {
	case "AssumeRoleWithWebIdentity":
		f.tokens = append(f.tokens, form.Get("WebIdentityToken"))
		if form.Get("RoleArn") != roleArn {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`<ErrorResponse><Error><Type>Sender</Type><Code>AccessDenied</Code><Message>Not authorized to perform sts:AssumeRoleWithWebIdentity</Message></Error><RequestId>1</RequestId></ErrorResponse>`))
			return
		}
		_, _ = fmt.Fprintf(w, `<AssumeRoleWithWebIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><AssumeRoleWithWebIdentityResult>`+
			`<Credentials><AccessKeyId>ASIAKEYLESSEXAMPLE</AccessKeyId><SecretAccessKey>temporary-secret</SecretAccessKey><SessionToken>session</SessionToken><Expiration>%s</Expiration></Credentials>`+
			`<AssumedRoleUser><Arn>arn:aws:sts::111111111111:assumed-role/loomplane/%s</Arn><AssumedRoleId>AROAEXAMPLE:x</AssumedRoleId></AssumedRoleUser>`+
			`</AssumeRoleWithWebIdentityResult><ResponseMetadata><RequestId>1</RequestId></ResponseMetadata></AssumeRoleWithWebIdentityResponse>`,
			time.Now().Add(time.Hour).UTC().Format(time.RFC3339), form.Get("RoleSessionName"))
	case "GetCallerIdentity":
		arn := "arn:aws:iam::111111111111:user/loomplane"
		if keyless {
			arn = "arn:aws:sts::111111111111:assumed-role/loomplane/loomplane-session"
		}
		_, _ = fmt.Fprintf(w, `<GetCallerIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><GetCallerIdentityResult>`+
			`<Arn>%s</Arn><UserId>AIDAEXAMPLE</UserId><Account>111111111111</Account>`+
			`</GetCallerIdentityResult><ResponseMetadata><RequestId>1</RequestId></ResponseMetadata></GetCallerIdentityResponse>`, arn)
	case "SimulatePrincipalPolicy":
		f.simulated = append(f.simulated, form.Get("PolicySourceArn"))
		var b strings.Builder
		b.WriteString(`<SimulatePrincipalPolicyResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/"><SimulatePrincipalPolicyResult><IsTruncated>false</IsTruncated><EvaluationResults>`)
		for i := 1; form.Get(fmt.Sprintf("ActionNames.member.%d", i)) != ""; i++ {
			fmt.Fprintf(&b, `<member><EvalActionName>%s</EvalActionName><EvalResourceName>*</EvalResourceName><EvalDecision>allowed</EvalDecision></member>`, form.Get(fmt.Sprintf("ActionNames.member.%d", i)))
		}
		b.WriteString(`</EvaluationResults></SimulatePrincipalPolicyResult><ResponseMetadata><RequestId>1</RequestId></ResponseMetadata></SimulatePrincipalPolicyResponse>`)
		_, _ = w.Write([]byte(b.String()))
	default:
		w.WriteHeader(http.StatusBadRequest)
	}
}

// ticketMinter mints for a handout with the fake issuer's ticket only.
type ticketMinter struct{}

func (ticketMinter) Mint(_ context.Context, h keyless.Handout) (string, error) {
	if h.Ticket != fakeTicket(h.Subject, h.Audience) {
		return "", errors.New("not a ticket the issuer made")
	}
	return "minted-for " + h.Subject, nil
}

// A key and the role's trust reach the same account and region, so a
// connection moves to keyless and keeps its ID, and every setting it had
// is in the handout a deployment gets.
func TestAWSKeylessLandsOnTheSameIdentityAsAKey(t *testing.T) {
	fake := &fakeAWSAccount{}
	srv := httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(srv.Close)
	client := aws.New(srv.URL).WithMinter(ticketMinter{})

	r := newKeylessRig(t)
	r.s.checkers[AWS] = AWSChecker{Client: client}
	r.s.KeylessClouds[AWS] = client
	settings := map[string]string{
		"region": "us-east-1", "certificate_arn": "arn:aws:acm:us-east-1:111111111111:certificate/abc",
		"rds_recovery_vpc": "vpc-1", "rds_recovery_subnet_group": "private", "rds_recovery_security_group": "sg-copy", "rds_recovery_verifier_group": "sg-verify",
	}
	key := `{"access_key_id":"AKIAIOSFODNN7EXAMPLE","secret_access_key":"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}`
	c, err := r.s.Create(t.Context(), starter, CreateInput{Cloud: AWS, Name: "Prod", Credential: key, Settings: settings})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if c.Account != "111111111111" || c.Details["access_key"] == "" {
		t.Fatalf("key connection: %+v", c)
	}

	moving := maps.Clone(settings)
	moving["role_arn"] = roleArn
	got, err := r.s.ReplaceKeyless(t.Context(), starter, c.ID, moving)
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	if got.ID != c.ID || got.Method != MethodKeyless || got.Status != StatusReady || got.Account != c.Account ||
		AccountIdentity(AWS, got.Account, got.Details) != AccountIdentity(AWS, c.Account, c.Details) {
		t.Fatalf("keyless connection: %+v", got)
	}
	if _, ok := got.Details["access_key"]; ok {
		t.Fatalf("details still name the key: %v", got.Details)
	}
	for k, v := range moving {
		if got.Details[k] != v {
			t.Errorf("details lost %s: %q", k, got.Details[k])
		}
	}
	if len(fake.simulated) != 2 || fake.simulated[0] != "arn:aws:iam::111111111111:user/loomplane" || fake.simulated[1] != roleArn {
		t.Fatalf("simulated: %v", fake.simulated)
	}
	if len(fake.tokens) != 2 || fake.tokens[0] != "minted-for "+keyless.Subject("org", c.ID) {
		t.Fatalf("assumed the role %d times, with the minted token: %v", len(fake.tokens), len(fake.tokens) > 0 && fake.tokens[0] == "minted-for "+keyless.Subject("org", c.ID))
	}

	// A deployment's handout carries every setting, and parses as a key.
	_, cred, err := r.s.Credential(t.Context(), "org", c.ID)
	if err != nil {
		t.Fatal(err)
	}
	h, _, _ := keyless.Parse(cred)
	for k, v := range moving {
		if h.Settings[k] != v {
			t.Errorf("handout lost %s: %q", k, h.Settings[k])
		}
	}
	if k, err := aws.ParseKey(cred); err != nil || !k.Keyless() || k.RoleARN != roleArn {
		t.Fatalf("handout key: %v", err)
	}
	if again, err := r.s.Recheck(t.Context(), starter, c.ID); err != nil || again.Status != StatusReady || again.Details["certificate_arn"] != settings["certificate_arn"] {
		t.Fatalf("recheck: %+v %v", again, err)
	}

	// A role in another account isn't the same identity.
	elsewhere := maps.Clone(moving)
	elsewhere["role_arn"] = otherRoleArn
	if _, err := r.s.ReplaceKeyless(t.Context(), starter, c.ID, elsewhere); !errors.Is(err, ErrInvalid) {
		t.Fatalf("other role: %v", err)
	}
	if !(AWSChecker{}).IsGrant(cred) {
		t.Fatal("a keyless handout isn't a grant")
	}
}

// Moving to keyless with only the role and region keeps the optional
// settings the connection already had, such as its certificate.
func TestSwitchToKeylessKeepsTheCertificate(t *testing.T) {
	fake := &fakeAWSAccount{}
	srv := httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(srv.Close)
	client := aws.New(srv.URL).WithMinter(ticketMinter{})
	r := newKeylessRig(t)
	r.s.checkers[AWS] = AWSChecker{Client: client}
	r.s.KeylessClouds[AWS] = client
	cert := "arn:aws:acm:us-east-1:111111111111:certificate/abc"
	key := `{"access_key_id":"AKIAIOSFODNN7EXAMPLE","secret_access_key":"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}`
	c, err := r.s.Create(t.Context(), starter, CreateInput{Cloud: AWS, Name: "Prod", Credential: key, Settings: map[string]string{"region": "us-east-1", "certificate_arn": cert}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := r.s.ReplaceKeyless(t.Context(), starter, c.ID, map[string]string{"role_arn": roleArn, "region": "us-east-1"})
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	if got.Details["certificate_arn"] != cert || got.Details["role_arn"] != roleArn {
		t.Fatalf("details: %v", got.Details)
	}
	if _, ok := got.Details["access_key"]; ok {
		t.Fatalf("details still name the key: %v", got.Details)
	}
}
