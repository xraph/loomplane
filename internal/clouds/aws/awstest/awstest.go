// Package awstest fakes the AWS APIs Loomplane checks an access key
// with, for tests: STS's GetCallerIdentity and IAM's policy simulator.
// Point aws.New at its URL.
package awstest

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
)

// Account and ARN are who every key is.
const (
	Account = "123456789012"
	ARN     = "arn:aws:iam::" + Account + ":user/loomplane"
)

// Key is an access key the fake accepts, as Loomplane stores one: AWS's
// own documentation example.
const Key = `{"access_key_id":"AKIAIOSFODNN7EXAMPLE","secret_access_key":"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}` //nolint:gosec // G101: AWS's documentation example key, for tests

// IAM answers GetCallerIdentity and SimulatePrincipalPolicy. The
// simulator denies what Deny names and allows the rest.
type IAM struct {
	URL string

	mu     sync.Mutex
	denied []string
	asked  [][]string
	refuse bool
}

// NewIAM starts a fake that denies denied, and stops it with the test.
func NewIAM(t testing.TB, denied ...string) *IAM {
	t.Helper()
	f := &IAM{denied: denied}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	f.URL = srv.URL
	return f
}

// Deny makes the simulator deny actions, and only those.
func (f *IAM) Deny(actions ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.denied = actions
}

// Refuse makes the key not allowed to run the simulator at all.
func (f *IAM) Refuse() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refuse = true
}

// Asked is each simulator call's actions, in order.
func (f *IAM) Asked() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.asked)
}

func (f *IAM) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body) //nolint:errcheck // a short test request
	form, err := url.ParseQuery(string(body))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	switch form.Get("Action") {
	case "GetCallerIdentity":
		_, _ = fmt.Fprintf(w, `<GetCallerIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><GetCallerIdentityResult>`+
			`<Arn>%s</Arn><UserId>AIDAEXAMPLE</UserId><Account>%s</Account></GetCallerIdentityResult>`+
			`<ResponseMetadata><RequestId>1</RequestId></ResponseMetadata></GetCallerIdentityResponse>`, ARN, Account)
	case "SimulatePrincipalPolicy":
		f.simulate(w, form)
	default:
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, `<ErrorResponse><Error><Code>InvalidAction</Code><Message>%s</Message></Error></ErrorResponse>`, form.Get("Action"))
	}
}

func (f *IAM) simulate(w http.ResponseWriter, form url.Values) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.refuse {
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprint(w, `<ErrorResponse><Error><Type>Sender</Type><Code>AccessDenied</Code><Message>not allowed</Message></Error><RequestId>1</RequestId></ErrorResponse>`)
		return
	}
	var b strings.Builder
	b.WriteString(`<SimulatePrincipalPolicyResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/"><SimulatePrincipalPolicyResult><IsTruncated>false</IsTruncated><EvaluationResults>`)
	var asked []string
	for i := 1; ; i++ {
		action := form.Get(fmt.Sprintf("ActionNames.member.%d", i))
		if action == "" {
			break
		}
		asked = append(asked, action)
		decision := "allowed"
		if slices.Contains(f.denied, action) {
			decision = "implicitDeny"
		}
		fmt.Fprintf(&b, `<member><EvalActionName>%s</EvalActionName><EvalResourceName>*</EvalResourceName><EvalDecision>%s</EvalDecision></member>`, action, decision)
	}
	f.asked = append(f.asked, asked)
	b.WriteString(`</EvaluationResults></SimulatePrincipalPolicyResult><ResponseMetadata><RequestId>1</RequestId></ResponseMetadata></SimulatePrincipalPolicyResponse>`)
	_, _ = fmt.Fprint(w, b.String())
}
