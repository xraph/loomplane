//go:build integration

package integration

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
	"time"

	"github.com/xraph/loomplane/internal/clouds/aws"
	"github.com/xraph/loomplane/internal/config"
)

const (
	awsKeyID   = "AKIAIOSFODNN7EXAMPLE"
	awsSecret  = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	awsAccount = "123456789012"
)

// fakeAWS answers the AWS APIs Loomplane calls, on one endpoint: the
// query APIs (STS, IAM, EC2, ELB, RDS) by their Action, and the JSON ones
// (ECS, ECR, CodeBuild, Logs, Secrets Manager) by their target header.
type fakeAWS struct {
	srv     *httptest.Server
	mu      sync.Mutex
	denied  []string
	json    map[string]func(body []byte) (int, string)
	query   map[string]func(form url.Values) (int, string)
	rest    http.Handler
	targets []string
}

func newFakeAWS(t *testing.T) *fakeAWS {
	t.Helper()
	f := &fakeAWS{json: map[string]func([]byte) (int, string){}, query: map[string]func(url.Values) (int, string){}}
	f.query["GetCallerIdentity"] = func(url.Values) (int, string) {
		return 200, `<GetCallerIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><GetCallerIdentityResult>` +
			`<Arn>arn:aws:iam::` + awsAccount + `:user/loomplane</Arn><UserId>AIDAEXAMPLE</UserId><Account>` + awsAccount + `</Account>` +
			`</GetCallerIdentityResult><ResponseMetadata><RequestId>1</RequestId></ResponseMetadata></GetCallerIdentityResponse>`
	}
	f.query["SimulatePrincipalPolicy"] = func(form url.Values) (int, string) {
		var b strings.Builder
		b.WriteString(`<SimulatePrincipalPolicyResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/"><SimulatePrincipalPolicyResult><IsTruncated>false</IsTruncated><EvaluationResults>`)
		for i := 1; ; i++ {
			action := form.Get(fmt.Sprintf("ActionNames.member.%d", i))
			if action == "" {
				break
			}
			decision := "allowed"
			f.mu.Lock()
			if slices.Contains(f.denied, action) {
				decision = "implicitDeny"
			}
			f.mu.Unlock()
			fmt.Fprintf(&b, `<member><EvalActionName>%s</EvalActionName><EvalResourceName>*</EvalResourceName><EvalDecision>%s</EvalDecision></member>`, action, decision)
		}
		b.WriteString(`</EvaluationResults></SimulatePrincipalPolicyResult><ResponseMetadata><RequestId>1</RequestId></ResponseMetadata></SimulatePrincipalPolicyResponse>`)
		return 200, b.String()
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAWS) serve(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential="+awsKeyID+"/") {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`<ErrorResponse><Error><Type>Sender</Type><Code>InvalidClientTokenId</Code><Message>The security token included in the request is invalid.</Message></Error><RequestId>1</RequestId></ErrorResponse>`))
		return
	}
	body, _ := io.ReadAll(r.Body)
	if target := r.Header.Get("X-Amz-Target"); target != "" {
		f.mu.Lock()
		f.targets = append(f.targets, target)
		handler := f.json[target]
		f.mu.Unlock()
		if handler == nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"__type":"UnknownOperationException","message":"` + target + `"}`))
			return
		}
		status, out := handler(body)
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(out))
		return
	}
	if r.Method == http.MethodPost && strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		form, _ := url.ParseQuery(string(body))
		f.mu.Lock()
		handler := f.query[form.Get("Action")]
		f.targets = append(f.targets, form.Get("Action"))
		f.mu.Unlock()
		if handler == nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`<ErrorResponse><Error><Code>InvalidAction</Code><Message>` + form.Get("Action") + `</Message></Error></ErrorResponse>`))
			return
		}
		status, out := handler(form)
		w.Header().Set("Content-Type", "text/xml")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(out))
		return
	}
	if f.rest != nil {
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		f.rest.ServeHTTP(w, r)
		return
	}
	http.NotFound(w, r)
}

func awsKey(keyID string) string {
	return `{"access_key_id":"` + keyID + `","secret_access_key":"` + awsSecret + `"}`
}

func TestConnections_anAWSKeyIsCheckedWithSTSAndIAM(t *testing.T) {
	fake := newFakeAWS(t)
	fake.denied = []string{"iam:CreateRole", "rds:CreateDBInstance"}
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	cfg.Clouds.AWSEndpoint = fake.srv.URL
	r := start(t, cfg)
	defer r.stop()

	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	var auth struct {
		SessionToken string `json:"session_token"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/signup", map[string]string{"email": "ada@example.com", "password": "Correct-horse-battery-9"}, &auth)
	c.token = auth.SessionToken
	var org struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Acme", "slug": "acme"}, &org)
	c.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, nil)

	settings := map[string]string{"region": "us-east-1"}
	// A malformed key, a missing region and a key AWS refuses are never
	// stored.
	c.must(http.StatusUnprocessableEntity, http.MethodPost, "/api/v1/connections", map[string]any{"cloud": "aws", "name": "Prod", "credential": "AKIA", "settings": settings}, nil)
	c.must(http.StatusUnprocessableEntity, http.MethodPost, "/api/v1/connections", map[string]any{"cloud": "aws", "name": "Prod", "credential": awsKey(awsKeyID)}, nil)
	c.must(http.StatusUnprocessableEntity, http.MethodPost, "/api/v1/connections", map[string]any{"cloud": "aws", "name": "Prod", "credential": awsKey("AKIAWRONGKEY0000"), "settings": settings}, nil)

	var conn connectionBody
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections", map[string]any{"cloud": "aws", "name": "Prod", "credential": awsKey(awsKeyID), "settings": settings}, &conn)
	if conn.Status != "incomplete" || conn.Account != awsAccount || conn.Details["region"] != "us-east-1" ||
		conn.Details["access_key"] != "…MPLE" || !slices.Equal(conn.Missing, []string{"iam:CreateRole", "rds:CreateDBInstance"}) {
		t.Fatalf("aws connection %+v", conn)
	}
	if body := c.raw(http.MethodGet, "/api/v1/connections"); strings.Contains(body, awsSecret) || strings.Contains(body, awsKeyID) {
		t.Fatalf("the key leaked: %s", body)
	}
	fake.mu.Lock()
	fake.denied = nil
	fake.mu.Unlock()
	c.must(http.StatusOK, http.MethodPost, "/api/v1/connections/"+conn.ID+"/check", nil, &conn)
	if conn.Status != "ready" {
		t.Fatalf("rechecked %+v", conn)
	}
	if len(aws.Actions) == 0 {
		t.Fatal("no actions to check")
	}
}
