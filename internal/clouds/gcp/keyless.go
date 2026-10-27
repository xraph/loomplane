package gcp

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google/externalaccount"

	"github.com/xraph/loomplane/internal/clouds/keyless"
	"github.com/xraph/loomplane/internal/manageddb"
)

const (
	// audiencePrefix comes before a workload provider's name in the aud
	// Google's STS expects.
	audiencePrefix = "//iam.googleapis.com/"
	// jwtTokenType is the kind of token STS is handed: Loomplane's own
	// signed JWT.
	jwtTokenType = "urn:ietf:params:oauth:token-type:jwt" //nolint:gosec // G101: a token type's name, not a credential
	// mintTimeout bounds minting a token, so a stuck issuer can't hold up
	// a session's renewal for good.
	mintTimeout = 30 * time.Second
	// maxReason caps how much of Google's reason for an answer an error
	// keeps.
	maxReason = 200
)

var (
	workloadProviderRE = regexp.MustCompile(`^projects/\d+/locations/global/workloadIdentityPools/[a-z0-9-]{4,32}/providers/[a-z0-9-]{4,32}$`)
	// serviceAccountRE is a service account's email. Its name is 6 to 30
	// characters, and its domain is its project's, which a
	// domain-scoped project gives dots.
	serviceAccountRE = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]@[a-z0-9][a-z0-9.-]{0,126}\.iam\.gserviceaccount\.com$`)
	// jwtRE is anything shaped like a signed token, which an error never
	// repeats.
	jwtRE = regexp.MustCompile(`eyJ[\w-]*\.[\w-]*\.[\w-]*`)
)

// CheckWorkloadProvider tells whether s is a workload identity pool
// provider's full name, such as
// projects/123456789012/locations/global/workloadIdentityPools/loomplane/providers/loomplane.
func CheckWorkloadProvider(s string) error {
	if s == "" {
		return errors.New("give the full name of the workload identity provider that trusts Loomplane, such as projects/123456789012/locations/global/workloadIdentityPools/loomplane/providers/loomplane")
	}
	if !workloadProviderRE.MatchString(s) {
		return fmt.Errorf("%q isn't a workload identity provider's full name, such as projects/123456789012/locations/global/workloadIdentityPools/loomplane/providers/loomplane", s)
	}
	return nil
}

// CheckServiceAccount tells whether s is a service account's email, such
// as loomplane@my-project.iam.gserviceaccount.com.
func CheckServiceAccount(s string) error {
	if !serviceAccountRE.MatchString(s) {
		return fmt.Errorf("%q isn't a service account's email, such as loomplane@my-project.iam.gserviceaccount.com", s)
	}
	return nil
}

// WithMinter returns a copy of the client that mints keyless keys' tokens
// with m.
func (c *Client) WithMinter(m keyless.Minter) *Client {
	configured := *c
	configured.minter = m
	return &configured
}

// keylessKey reads a handout's project, workload provider and service
// account. data is the handout as stored, which the Cloud SQL connector
// reads again.
func keylessKey(h keyless.Handout, data []byte) (*Key, error) {
	project := strings.TrimSpace(h.Settings["project_id"])
	provider := strings.TrimSpace(h.Settings["workload_provider"])
	account := strings.TrimSpace(h.Settings["service_account"])
	if err := CheckProjectID(project); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidKey, err)
	}
	if err := CheckWorkloadProvider(provider); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidKey, err)
	}
	if err := CheckServiceAccount(account); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidKey, err)
	}
	if h.Audience != audiencePrefix+provider {
		return nil, fmt.Errorf("%w: the keyless handout's audience isn't its workload provider's", ErrInvalidKey)
	}
	return &Key{ProjectID: project, ClientEmail: account, Keyless: true, WorkloadProvider: provider, handout: &h, raw: data}, nil
}

// keylessTokens trades a token minted for key's handout at Google's STS,
// then impersonates the service account with what STS gives back. It
// mints again each time the service account's token expires.
func (c *Client) keylessTokens(ctx context.Context, key *Key) (oauth2.TokenSource, error) {
	if c.minter == nil {
		return nil, keyless.ErrNoMinter
	}
	stsURL := c.url("sts.googleapis.com", "/v1/token")
	iamURL := c.url("iamcredentials.googleapis.com", "/v1/projects/-/serviceAccounts/"+key.ClientEmail+":generateAccessToken")
	src := &keylessSource{}
	watch, err := newExchangeWatch(c.base.Transport, src, stsURL, iamURL)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidKey, err)
	}
	// Both trades go through the base client's transport, watched, and
	// share its timeout.
	ctx = context.WithValue(ctx, oauth2.HTTPClient, &http.Client{Timeout: c.base.Timeout, Transport: watch})
	src.ts, err = externalaccount.NewTokenSource(ctx, externalaccount.Config{
		Audience:                       audiencePrefix + key.WorkloadProvider,
		SubjectTokenType:               jwtTokenType,
		TokenURL:                       stsURL,
		ServiceAccountImpersonationURL: iamURL,
		Scopes:                         []string{Scope},
		SubjectTokenSupplier:           supplier{minter: c.minter, handout: *key.handout, src: src},
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidKey, err)
	}
	return src, nil
}

// keylessSource is a keyless session's token source. x/oauth2 flattens
// why a trade failed into text, which can't tell a refusal from an
// outage, so the supplier and the transport note what went wrong here,
// and Token returns that instead.
type keylessSource struct {
	ts oauth2.TokenSource
	// call holds one trade at a time, so a note is the trade's that
	// made it.
	call sync.Mutex
	mu   sync.Mutex
	why  error
}

// note keeps the first thing that went wrong in a trade.
func (k *keylessSource) note(err error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.why == nil {
		k.why = err
	}
}

func (k *keylessSource) Token() (*oauth2.Token, error) {
	k.call.Lock()
	defer k.call.Unlock()
	k.mu.Lock()
	k.why = nil
	k.mu.Unlock()
	tok, err := k.ts.Token()
	if err == nil {
		return tok, nil
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	// What x/oauth2 says is never passed on: it can quote what Google
	// answered.
	if k.why != nil {
		return nil, k.why
	}
	return nil, errors.New("google cloud couldn't trade Loomplane's token for the service account's")
}

// supplier mints a token each time STS needs one.
type supplier struct {
	minter  keyless.Minter
	handout keyless.Handout
	src     *keylessSource
}

// SubjectToken implements externalaccount.SubjectTokenSupplier.
func (s supplier) SubjectToken(ctx context.Context, _ externalaccount.SupplierOptions) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, mintTimeout)
	defer cancel()
	token, err := s.minter.Mint(ctx, s.handout)
	if err != nil {
		err = fmt.Errorf("mint a keyless token: %w", err)
		s.src.note(err)
		return "", err
	}
	return token, nil
}

// exchangeWatch is the transport of a keyless session's trades. It notes
// what STS and IAM Credentials answer when they don't give a token, and
// leaves every other request alone.
type exchangeWatch struct {
	next     http.RoundTripper
	src      *keylessSource
	sts, iam *url.URL
}

func newExchangeWatch(next http.RoundTripper, src *keylessSource, stsURL, iamURL string) (exchangeWatch, error) {
	sts, err := url.Parse(stsURL)
	if err != nil {
		return exchangeWatch{}, err
	}
	iam, err := url.Parse(iamURL)
	if err != nil {
		return exchangeWatch{}, err
	}
	if next == nil {
		next = http.DefaultTransport
	}
	return exchangeWatch{next: next, src: src, sts: sts, iam: iam}, nil
}

func (w exchangeWatch) RoundTrip(req *http.Request) (*http.Response, error) {
	at := func(u *url.URL) bool { return req.URL.Host == u.Host && req.URL.Path == u.Path }
	isSTS, isIAM := at(w.sts), at(w.iam)
	res, err := w.next.RoundTrip(req)
	if !isSTS && !isIAM {
		return res, err
	}
	service := "security token service"
	if isIAM {
		service = "IAM credentials service"
	}
	if err != nil {
		w.src.note(fmt.Errorf("google's %s didn't answer: %w", service, err))
		return res, err
	}
	if res.StatusCode < 300 {
		return res, nil
	}
	body, readErr := io.ReadAll(io.LimitReader(res.Body, maxResponse))
	_ = res.Body.Close() // replaced below; what was read is all that's used
	res.Body = io.NopCloser(bytes.NewReader(body))
	if readErr != nil {
		w.src.note(fmt.Errorf("google's %s answered %d, unreadably", service, res.StatusCode))
		return res, nil
	}
	if isSTS {
		w.src.note(stsAnswer(res.StatusCode, body))
	} else {
		w.src.note(iamAnswer(res.StatusCode, body))
	}
	return res, nil
}

// stsAnswer is why STS didn't trade the token. Only STS refusing the
// token itself is ErrRejected: a trust that's gone or never matched.
// STS not reaching Loomplane's issuer for its keys is Loomplane's to fix,
// and a retry may get past it, so it isn't.
func stsAnswer(status int, body []byte) error {
	var e struct {
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	_ = json.Unmarshal(body, &e) //nolint:errcheck // an answer that isn't JSON has no reason to keep
	why := cmp.Or(reason(e.Error, maxErrorCode), "no reason given")
	if d := reason(e.Description, maxReason); d != "" {
		why += ": " + d
	}
	refused := status == http.StatusBadRequest && (e.Error == "invalid_grant" || e.Error == "unauthorized_client")
	if refused && !issuerUnreachable(e.Description) {
		return fmt.Errorf("%w: google's security token service refused Loomplane's token: %s", ErrRejected, why)
	}
	return fmt.Errorf("google's security token service answered %d: %s", status, why)
}

// issuerUnreachable reports whether STS says it couldn't fetch the
// issuer's discovery document or keys, or couldn't read them.
func issuerUnreachable(description string) bool {
	d := strings.ToLower(description)
	return strings.Contains(d, "connecting to the given credential's issuer") || strings.Contains(d, "jwk")
}

// iamAnswer is why IAM Credentials didn't impersonate the service
// account. Only its 403 is ErrRejected: the connection isn't let act as
// the account, or the account doesn't exist.
func iamAnswer(status int, body []byte) error {
	var e struct {
		Error struct {
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &e) //nolint:errcheck // an answer that isn't JSON has no reason to keep
	why := cmp.Or(reason(e.Error.Message, maxReason), reason(e.Error.Status, maxErrorCode), "no reason given")
	if status == http.StatusForbidden && e.Error.Status == "PERMISSION_DENIED" {
		return fmt.Errorf("%w: google won't let this connection act as the service account: %s", ErrRejected, why)
	}
	return fmt.Errorf("google's IAM credentials service answered %d: %s", status, why)
}

// reason is s, kept to n bytes, with anything shaped like a token taken
// out.
func reason(s string, n int) string {
	s = jwtRE.ReplaceAllString(strings.TrimSpace(s), "[token]")
	if len(s) > n {
		s = s[:n]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return s
}

// KeylessSettings implements connections.KeylessCloud: the project, the
// workload provider that trusts Loomplane and the service account to act
// as, and the optional billing export and Cloud SQL recovery route a
// key's connection takes too.
func (c *Client) KeylessSettings(in map[string]string) (map[string]string, error) {
	project := strings.TrimSpace(in["project_id"])
	provider := strings.TrimSpace(in["workload_provider"])
	account := strings.TrimSpace(in["service_account"])
	if err := CheckProjectID(project); err != nil {
		return nil, err
	}
	if err := CheckWorkloadProvider(provider); err != nil {
		return nil, err
	}
	if err := CheckServiceAccount(account); err != nil {
		return nil, err
	}
	out := map[string]string{"project_id": project, "workload_provider": provider, "service_account": account}
	if table := strings.TrimSpace(in[BillingExportSetting]); table != "" {
		if err := CheckBillingTable(table); err != nil {
			return nil, err
		}
		out[BillingExportSetting] = table
	}
	route, err := manageddb.CloudSQLRecoverySettings(in)
	if err != nil {
		return nil, err
	}
	maps.Copy(out, route.Settings())
	return out, nil
}

// KeylessAudience implements connections.KeylessCloud: the workload
// provider's own name, which its trust expects by default.
func (c *Client) KeylessAudience(settings map[string]string) string {
	return audiencePrefix + strings.TrimSpace(settings["workload_provider"])
}

// KeylessSetup implements connections.KeylessCloud: make a workload
// identity pool with a provider that trusts subject alone, let subject
// act as a service account, and give the account what a deployment
// needs.
func (c *Client) KeylessSetup(issuer, subject string) []keyless.Step {
	return []keyless.Step{
		{
			Title:   "Create a workload identity pool",
			Body:    "In the Google Cloud project you're connecting, create a pool for Loomplane. You can run these commands in Cloud Shell, or anywhere gcloud is set to the project.",
			Command: "gcloud iam workload-identity-pools create loomplane --location=global",
		},
		{
			Title: "Add Loomplane as the pool's provider",
			Body: "The provider trusts tokens from Loomplane's issuer, and its condition lets in this connection alone. " +
				"You paste the provider's full name below. To print it, run gcloud iam workload-identity-pools providers describe loomplane --location=global --workload-identity-pool=loomplane --format='value(name)'.",
			Command: "gcloud iam workload-identity-pools providers create-oidc loomplane --location=global --workload-identity-pool=loomplane " +
				"--issuer-uri=" + strings.TrimRight(issuer, "/") + " --attribute-mapping=google.subject=assertion.sub --attribute-condition=\"assertion.sub=='" + subject + "'\"",
		},
		{
			Title: "Let this connection act as a service account",
			Body: "Create a service account for Loomplane if you don't have one. Then put its email in place of <service-account>, " +
				"and your project's number, from the console's dashboard, in place of <project-number>.",
			Command: "gcloud iam service-accounts add-iam-policy-binding <service-account> --role=roles/iam.workloadIdentityUser " +
				"--member=principal://iam.googleapis.com/projects/<project-number>/locations/global/workloadIdentityPools/loomplane/subject/" + subject,
		},
		{
			Title: "Give the service account Loomplane's permissions",
			Body: "This makes a custom role with the permissions a deployment needs and grants it to the service account. Put your project ID in place of <project-id>. " +
				"Then paste the project ID, the provider's full name and the service account's email below, and Loomplane checks that Google lets this connection act as the account.",
			Command: "gcloud iam roles create loomplane --project=<project-id> --title=Loomplane --permissions=" + strings.Join(Permissions, ",") + "\n" +
				"gcloud projects add-iam-policy-binding <project-id> --member=serviceAccount:<service-account> --role=projects/<project-id>/roles/loomplane",
		},
	}
}
