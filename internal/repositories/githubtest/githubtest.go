// Package githubtest is a fake GitHub for tests: the parts a GitHub App
// touches to read an installation, mint installation tokens and keep
// issues. It checks the App's JWT with its own key and every repository
// call against the token's scope, so a test sees the refusals GitHub
// would give. Nothing in it reaches the network outside the test.
package githubtest

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/repositories"
)

// AppID is the App's ID in AppConfig.
const AppID = 4242

var (
	keyOnce sync.Once
	key     *rsa.PrivateKey
	errKey  error
)

// appKey is one RSA key for every Server in the test binary, since
// generating one is slow.
func appKey() (*rsa.PrivateKey, error) {
	keyOnce.Do(func() { key, errKey = rsa.GenerateKey(rand.Reader, 2048) })
	return key, errKey
}

// IssueRecord is an issue, or a pull request, as the fake keeps it.
type IssueRecord struct {
	Number      int      `json:"number"`
	Title       string   `json:"title"`
	Body        string   `json:"body"`
	State       string   `json:"state"`
	StateReason string   `json:"state_reason"`
	Labels      []string `json:"labels"`
	Comments    []string `json:"comments"`
	PullRequest bool     `json:"pull_request"`
}

// TokenRequest is what one access_tokens call asked for.
type TokenRequest struct {
	InstallationID int64             `json:"installation_id"`
	Repositories   []string          `json:"repositories"`
	Permissions    map[string]string `json:"permissions"`
}

type installation struct {
	account string
	perms   map[string]string
}

type token struct {
	installation int64
	repos        []string // names; empty means every repository
	perms        map[string]string
}

type repository struct {
	labels map[string]bool
	issues []*IssueRecord
}

// Server is the fake GitHub.
type Server struct {
	srv *httptest.Server
	key *rsa.PrivateKey

	mu            sync.Mutex
	installations map[int64]*installation
	tokens        map[string]*token
	repos         map[string]*repository // by lowercased owner/name
	fail          map[string][]int       // path -> statuses to answer next
	requests      []string
	tokenRequests []TokenRequest
	minted        int
}

// New starts a fake GitHub that closes when the test ends.
func New(t testing.TB) *Server {
	t.Helper()
	k, err := appKey()
	if err != nil {
		t.Fatalf("githubtest: generate key: %v", err)
	}
	s := &Server{
		key: k, installations: map[int64]*installation{}, tokens: map[string]*token{},
		repos: map[string]*repository{}, fail: map[string][]int{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /app/installations/{id}", s.app(s.getInstallation))
	mux.HandleFunc("POST /app/installations/{id}/access_tokens", s.app(s.accessToken))
	mux.HandleFunc("POST /repos/{owner}/{repo}/labels", s.issuesEndpoint("write", s.createLabel))
	mux.HandleFunc("GET /repos/{owner}/{repo}/issues", s.issuesEndpoint("read", s.listIssues))
	mux.HandleFunc("POST /repos/{owner}/{repo}/issues", s.issuesEndpoint("write", s.createIssue))
	mux.HandleFunc("PATCH /repos/{owner}/{repo}/issues/{number}", s.issuesEndpoint("write", s.editIssue))
	mux.HandleFunc("POST /repos/{owner}/{repo}/issues/{number}/comments", s.issuesEndpoint("write", s.comment))
	s.srv = httptest.NewServer(s.record(mux))
	t.Cleanup(s.srv.Close)
	return s
}

// URL is the fake's address, both its API and its web URL.
func (s *Server) URL() string { return s.srv.URL }

// AppConfig is an App configuration whose JWT the fake accepts.
func (s *Server) AppConfig() repositories.AppConfig {
	return repositories.AppConfig{
		ID: AppID, Slug: "loomplane-test", ClientID: "Iv1.test", ClientSecret: "test-secret",
		PrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(s.key)})),
		APIURL:     s.srv.URL, WebURL: s.srv.URL,
	}
}

// AddInstallation installs the App on an organization account with
// perms. The installation can see every repository of the account.
func (s *Server) AddInstallation(id int64, account string, perms map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.installations[id] = &installation{account: account, perms: clone(perms)}
}

// SetPermissions changes what an installation has accepted, as an owner
// does on GitHub.
func (s *Server) SetPermissions(id int64, perms map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if inst, ok := s.installations[id]; ok {
		inst.perms = clone(perms)
	}
}

// AddPullRequest adds a pull request, which the issues list returns too.
func (s *Server) AddPullRequest(repo, title, body string, labels ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.repoLocked(repo)
	r.issues = append(r.issues, &IssueRecord{
		Number: len(r.issues) + 1, Title: title, Body: body, State: "open",
		Labels: slices.Clone(labels), Comments: []string{}, PullRequest: true,
	})
}

// Issues returns a copy of repo's issues and pull requests, oldest first.
func (s *Server) Issues(repo string) []IssueRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.repos[strings.ToLower(repo)]
	if !ok {
		return nil
	}
	out := make([]IssueRecord, 0, len(r.issues))
	for _, is := range r.issues {
		c := *is
		c.Labels, c.Comments = slices.Clone(is.Labels), slices.Clone(is.Comments)
		out = append(out, c)
	}
	return out
}

// FailNext makes the next request to path answer status instead.
func (s *Server) FailNext(path string, status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fail[path] = append(s.fail[path], status)
}

// Requests lists every request so far as "METHOD /path?query".
func (s *Server) Requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.requests)
}

// TokenRequests lists what every access_tokens call asked for, refused
// ones included.
func (s *Server) TokenRequests() []TokenRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.tokenRequests)
}

func (s *Server) record(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.requests = append(s.requests, r.Method+" "+r.URL.RequestURI())
		var status int
		if q := s.fail[r.URL.Path]; len(q) > 0 {
			status, s.fail[r.URL.Path] = q[0], q[1:]
		}
		s.mu.Unlock()
		if status != 0 {
			writeJSON(w, status, map[string]string{"message": "githubtest failed this request"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// app wraps an endpoint that takes the App's own JWT.
func (s *Server) app(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.validJWT(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "A JSON web token could not be decoded"})
			return
		}
		h(w, r)
	}
}

func (s *Server) validJWT(r *http.Request) bool {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	parts := strings.Split(tok, ".")
	if !ok || len(parts) != 3 {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(&s.key.PublicKey, crypto.SHA256, sum[:], sig) != nil {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var claims struct {
		Iss string `json:"iss"`
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
	}
	return json.Unmarshal(raw, &claims) == nil && claims.Iss == strconv.Itoa(AppID) && claims.Exp-claims.Iat <= 600
}

func (s *Server) installationFor(w http.ResponseWriter, r *http.Request) (int64, *installation, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	inst, ok := s.installations[id]
	if err != nil || !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		return 0, nil, false
	}
	return id, inst, true
}

func (s *Server) getInstallation(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, inst, ok := s.installationFor(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":          id,
		"account":     map[string]string{"login": inst.account, "type": "Organization"},
		"permissions": inst.perms,
	})
}

func (s *Server) accessToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Repositories []string          `json:"repositories"`
		Permissions  map[string]string `json:"permissions"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "Problems parsing JSON"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id, inst, ok := s.installationFor(w, r)
	if !ok {
		return
	}
	s.tokenRequests = append(s.tokenRequests, TokenRequest{InstallationID: id, Repositories: body.Repositories, Permissions: body.Permissions})
	for name, level := range body.Permissions {
		if !grants(inst.perms, name, level) {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"message": "The permissions requested are not granted to this installation."})
			return
		}
	}
	perms := body.Permissions
	if len(perms) == 0 {
		perms = inst.perms
	}
	s.minted++
	tok := fmt.Sprintf("ghs_githubtest_%d", s.minted)
	s.tokens[tok] = &token{installation: id, repos: body.Repositories, perms: clone(perms)}
	writeJSON(w, http.StatusCreated, map[string]any{"token": tok, "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
}

// grants reports whether perms allow level of name. Every installation
// can read metadata, and write includes read.
func grants(perms map[string]string, name, level string) bool {
	if name == "metadata" && level == "read" {
		return true
	}
	have := perms[name]
	return have == level || (have == "write" && level == "read")
}

// issuesEndpoint wraps a repository endpoint that needs the Issues
// permission at level, from an installation token scoped to that
// repository.
func (s *Server) issuesEndpoint(level string, h func(http.ResponseWriter, *http.Request, *repository)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		raw, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		tok, ok := s.tokens[raw]
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Bad credentials"})
			return
		}
		inst := s.installations[tok.installation]
		owner, name := r.PathValue("owner"), r.PathValue("repo")
		if inst == nil || !strings.EqualFold(inst.account, owner) ||
			(len(tok.repos) > 0 && !slices.ContainsFunc(tok.repos, func(n string) bool { return strings.EqualFold(n, name) })) {
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
			return
		}
		if !grants(tok.perms, "issues", level) || !grants(inst.perms, "issues", level) {
			writeJSON(w, http.StatusForbidden, map[string]string{"message": "Resource not accessible by integration"})
			return
		}
		h(w, r, s.repoLocked(owner+"/"+name))
	}
}

func (s *Server) repoLocked(full string) *repository {
	k := strings.ToLower(full)
	r, ok := s.repos[k]
	if !ok {
		r = &repository{labels: map[string]bool{}}
		s.repos[k] = r
	}
	return r
}

func (s *Server) createLabel(w http.ResponseWriter, r *http.Request, repo *repository) {
	var body struct {
		Name        string `json:"name"`
		Color       string `json:"color"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"message": "Validation Failed", "errors": []map[string]string{{"resource": "Label", "code": "missing_field", "field": "name"}}})
		return
	}
	if repo.labels[strings.ToLower(body.Name)] {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"message": "Validation Failed", "errors": []map[string]string{{"resource": "Label", "code": "already_exists", "field": "name"}}})
		return
	}
	repo.labels[strings.ToLower(body.Name)] = true
	writeJSON(w, http.StatusCreated, map[string]string{"name": body.Name, "color": body.Color, "description": body.Description})
}

// listIssues pages through issues and pull requests, newest first, as
// GitHub sorts them by default.
func (s *Server) listIssues(w http.ResponseWriter, r *http.Request, repo *repository) {
	q := r.URL.Query()
	state := q.Get("state")
	if state == "" {
		state = "open"
	}
	var want []string
	if l := q.Get("labels"); l != "" {
		want = strings.Split(l, ",")
	}
	perPage := intParam(q.Get("per_page"), 30)
	if perPage > 100 {
		perPage = 30
	}
	page := intParam(q.Get("page"), 1)
	var matched []*IssueRecord
	for i := len(repo.issues) - 1; i >= 0; i-- {
		is := repo.issues[i]
		if state != "all" && is.State != state {
			continue
		}
		if !slices.ContainsFunc(want, func(l string) bool { return !slices.Contains(is.Labels, l) }) {
			matched = append(matched, is)
		}
	}
	out := []map[string]any{}
	for i := (page - 1) * perPage; i < len(matched) && i < page*perPage; i++ {
		out = append(out, s.issueJSON(r, matched[i]))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) createIssue(w http.ResponseWriter, r *http.Request, repo *repository) {
	var body struct {
		Title  string   `json:"title"`
		Body   string   `json:"body"`
		Labels []string `json:"labels"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Title == "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"message": "Validation Failed", "errors": []map[string]string{{"resource": "Issue", "code": "missing_field", "field": "title"}}})
		return
	}
	is := &IssueRecord{
		Number: len(repo.issues) + 1, Title: body.Title, Body: body.Body, State: "open",
		Labels: slices.Clone(body.Labels), Comments: []string{},
	}
	if is.Labels == nil {
		is.Labels = []string{}
	}
	repo.issues = append(repo.issues, is)
	writeJSON(w, http.StatusCreated, s.issueJSON(r, is))
}

func (s *Server) issue(w http.ResponseWriter, r *http.Request, repo *repository) (*IssueRecord, bool) {
	n, err := strconv.Atoi(r.PathValue("number"))
	if err != nil || n < 1 || n > len(repo.issues) {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		return nil, false
	}
	return repo.issues[n-1], true
}

func (s *Server) editIssue(w http.ResponseWriter, r *http.Request, repo *repository) {
	is, ok := s.issue(w, r, repo)
	if !ok {
		return
	}
	var body struct {
		Title       *string `json:"title"`
		Body        *string `json:"body"`
		State       *string `json:"state"`
		StateReason *string `json:"state_reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "Problems parsing JSON"})
		return
	}
	if body.State != nil && *body.State != "open" && *body.State != "closed" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"message": "Validation Failed", "errors": []map[string]string{{"resource": "Issue", "code": "invalid", "field": "state"}}})
		return
	}
	if body.Title != nil {
		is.Title = *body.Title
	}
	if body.Body != nil {
		is.Body = *body.Body
	}
	if body.State != nil {
		is.State = *body.State
	}
	if body.StateReason != nil {
		is.StateReason = *body.StateReason
	}
	writeJSON(w, http.StatusOK, s.issueJSON(r, is))
}

func (s *Server) comment(w http.ResponseWriter, r *http.Request, repo *repository) {
	is, ok := s.issue(w, r, repo)
	if !ok {
		return
	}
	var body struct {
		Body string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Body == "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"message": "Validation Failed", "errors": []map[string]string{{"resource": "IssueComment", "code": "missing_field", "field": "body"}}})
		return
	}
	is.Comments = append(is.Comments, body.Body)
	writeJSON(w, http.StatusCreated, map[string]any{"id": len(is.Comments), "body": body.Body})
}

func (s *Server) issueJSON(r *http.Request, is *IssueRecord) map[string]any {
	labels := make([]map[string]string, 0, len(is.Labels))
	for _, l := range is.Labels {
		labels = append(labels, map[string]string{"name": l})
	}
	kind := "issues"
	if is.PullRequest {
		kind = "pull"
	}
	out := map[string]any{
		"number":   is.Number,
		"title":    is.Title,
		"body":     is.Body,
		"state":    is.State,
		"labels":   labels,
		"html_url": fmt.Sprintf("%s/%s/%s/%s/%d", s.srv.URL, r.PathValue("owner"), r.PathValue("repo"), kind, is.Number),
	}
	if is.StateReason != "" {
		out["state_reason"] = is.StateReason
	}
	if is.PullRequest {
		out["pull_request"] = map[string]string{"url": fmt.Sprintf("%s/repos/%s/%s/pulls/%d", s.srv.URL, r.PathValue("owner"), r.PathValue("repo"), is.Number)}
	}
	return out
}

// intParam reads a positive number from a query, or def.
func intParam(raw string, def int) int {
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

func clone(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v) //nolint:errcheck // the test sees what it got
}
