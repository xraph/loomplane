package repositories

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// What checking an OAuth client with its host found.
const (
	// ClientAccepted: the host took the client and refused only the
	// made-up code.
	ClientAccepted = "accepted"
	// ClientRejected: the host doesn't know the client ID, or the secret is
	// wrong.
	ClientRejected = "rejected"
	// ClientRedirectMismatch: the host knows the client but doesn't allow
	// Loomplane's callback URL for it.
	ClientRedirectMismatch = "redirect_mismatch"
	// ClientUnreachable: the host's token endpoint couldn't be reached.
	ClientUnreachable = "unreachable"
	// ClientInconclusive: the host answered, but not in a way that says
	// either way.
	ClientInconclusive = "inconclusive"
)

// ClientCheck is what a check found. Detail is the host's own answer, with
// the secret taken out.
type ClientCheck struct {
	Result  string
	Summary string
	Detail  string
}

// ClientProbe is one check: a made-up code redeemed at TokenURL with the
// client. BasicAuth sends the client as a Basic header, as Bitbucket wants,
// instead of as form fields. RedirectURL is sent when it's set.
type ClientProbe struct {
	Host         string
	TokenURL     string
	RedirectURL  string
	ClientID     string
	ClientSecret string
	BasicAuth    bool
	// HTTP is the client to call with; nil uses one that follows no
	// redirect, since the request carries the secret.
	HTTP *http.Client
}

// probeCode is the code every check redeems. It's long and plainly fake.
const probeCode = "loomplane-console-client-check-not-a-real-code"

// probeTimeout bounds one check.
const probeTimeout = 15 * time.Second

var probeClient = &http.Client{
	Timeout: probeTimeout,
	// A token endpoint that redirects isn't one to send a secret on to.
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// CheckClient asks p's token endpoint whether it knows the client, by
// redeeming a code that can't exist. Hosts authenticate the client before
// they look at the code (RFC 6749 section 5.2), so the refusal says which of
// the two was wrong. Nothing is created at the host.
func CheckClient(ctx context.Context, p ClientProbe) ClientCheck {
	form := url.Values{"grant_type": {"authorization_code"}, "code": {probeCode}}
	if p.RedirectURL != "" {
		form.Set("redirect_uri", p.RedirectURL)
	}
	if !p.BasicAuth {
		form.Set("client_id", p.ClientID)
		form.Set("client_secret", p.ClientSecret)
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return ClientCheck{Result: ClientInconclusive, Summary: "The token URL isn't one Loomplane can call."}
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "loomplane")
	if p.BasicAuth {
		req.SetBasicAuth(p.ClientID, p.ClientSecret)
	}
	hc := p.HTTP
	if hc == nil {
		hc = probeClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return ClientCheck{Result: ClientUnreachable, Summary: "Couldn't reach " + p.Host + "'s token endpoint.",
			Detail: scrubSecret(err.Error(), p.ClientSecret)}
	}
	defer resp.Body.Close() // read-only body
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		body = nil // a body cut short says nothing; the status still does
	}
	code, msg := tokenError(body)
	out := classifyClient(p.Host, resp.StatusCode, code, msg)
	detail := code
	if msg != "" {
		detail = strings.TrimPrefix(code+": "+msg, ": ")
	}
	if detail != "" {
		out.Detail = scrubSecret(resp.Status+" · "+truncate(detail, 300), p.ClientSecret)
	}
	return out
}

// classifyClient reads a token endpoint's answer to the made-up code.
func classifyClient(host string, status int, code, msg string) ClientCheck {
	c, m := strings.ToLower(code), strings.ToLower(msg)
	switch {
	case c == "redirect_uri_mismatch" || strings.Contains(m, "redirect_uri") && strings.Contains(m, "mismatch"):
		return ClientCheck{Result: ClientRedirectMismatch,
			Summary: host + " knows this client, but Loomplane's callback URL isn't allowed for it. Add it in the app's settings."}
	case c == "invalid_client" || c == "unauthorized_client" || c == "incorrect_client_credentials" ||
		strings.Contains(c, "client_id") || strings.Contains(c, "client_secret") || strings.Contains(c, "bad_client"):
		return ClientCheck{Result: ClientRejected, Summary: host + " doesn't accept this client ID and secret."}
	case c == "invalid_grant" || c == "bad_verification_code" || c == "invalid_code" || strings.Contains(c, "code"):
		return ClientCheck{Result: ClientAccepted,
			Summary: host + " accepted the client and refused only the test code, as expected. If sign-ins still fail, check the callback URL registered with the app."}
	case status == http.StatusUnauthorized:
		return ClientCheck{Result: ClientRejected, Summary: host + " doesn't accept this client ID and secret."}
	}
	return ClientCheck{Result: ClientInconclusive,
		Summary: host + " answered, but not in a way that says whether the client is right. Connect with it to be sure."}
}

// tokenError pulls the error code and description out of a token
// endpoint's body, JSON or form-encoded.
func tokenError(body []byte) (code, msg string) {
	var j map[string]any
	if json.Unmarshal(body, &j) == nil {
		return stringOf(j["error"]), stringOf(j["error_description"])
	}
	if v, err := url.ParseQuery(string(body)); err == nil && v.Get("error") != "" {
		return v.Get("error"), v.Get("error_description")
	}
	return "", ""
}

func stringOf(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func scrubSecret(s, secret string) string {
	if secret == "" {
		return s
	}
	return strings.ReplaceAll(s, secret, "[secret]")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ErrAppKeyRejected is GitHub refusing a GitHub App's JWT: the App ID and
// the private key aren't a pair.
var ErrAppKeyRejected = errors.New("GitHub doesn't accept this App ID and private key together")

// Identify asks GitHub which App the App's own JWT is for, which proves its
// ID and private key are a pair, and returns the App's slug.
func (a *App) Identify(ctx context.Context) (string, error) {
	tok, err := a.jwt()
	if err != nil {
		return "", err
	}
	var body struct {
		Slug string `json:"slug"`
	}
	err = a.call(ctx, http.MethodGet, a.apiURL+"/app", tok, nil, &body)
	var se *statusError
	if errors.As(err, &se) && (se.status == http.StatusUnauthorized || se.status == http.StatusForbidden || se.status == http.StatusNotFound) {
		return "", ErrAppKeyRejected
	}
	if err != nil {
		return "", err
	}
	return body.Slug, nil
}
