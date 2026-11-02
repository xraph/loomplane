package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// maxBody caps what one Hugging Face response may hold. The largest model
// listings are well under a megabyte.
const maxBody = 32 << 20

// errNeedsToken means Hugging Face refused a file because the repo is gated
// and the request carried no token, or one whose account hasn't accepted
// the license.
var errNeedsToken = errors.New("gated on Hugging Face: run with a token whose account accepted the license")

// errRepoNotFound means Hugging Face has no repo by that ID, or none this
// request may see. It answers an anonymous request for a missing repo with
// 401, like a gated one, and tells the two apart only by X-Error-Code.
var errRepoNotFound = errors.New("no such repo on Hugging Face: check the ID")

// Values of the X-Error-Code header Hugging Face sets on a refusal.
const (
	hubRepoNotFound = "RepoNotFound"
	hubGatedRepo    = "GatedRepo"
)

// sibling is one file in a repo at a revision.
type sibling struct {
	Name string `json:"rfilename"`
	Size int64  `json:"size"`
	LFS  *struct {
		Size int64 `json:"size"`
	} `json:"lfs"`
}

func (s sibling) size() int64 {
	if s.Size == 0 && s.LFS != nil {
		return s.LFS.Size
	}
	return s.Size
}

// modelInfo is the part of GET /api/models/<repo>?blobs=true the tool reads.
type modelInfo struct {
	ID          string    `json:"id"`
	SHA         string    `json:"sha"`
	Gated       any       `json:"gated"` // false, "auto" or "manual"
	Siblings    []sibling `json:"siblings"`
	Safetensors *struct {
		Total int64 `json:"total"`
	} `json:"safetensors"`
	CardData struct {
		License any `json:"license"` // a string, or a list on a few repos
	} `json:"cardData"`
}

// gated reports whether downloading the repo's files needs an accepted
// license.
func (m *modelInfo) gated() bool {
	switch g := m.Gated.(type) {
	case bool:
		return g
	case string:
		return g != "" && g != "false"
	}
	return false
}

// license returns the repo's license tag, or "" when it has none or several.
func (m *modelInfo) license() string {
	return asString(m.CardData.License)
}

// hub talks to the Hugging Face Hub, one request at a time, at most one per
// interval, and waits out a 429 before trying again.
type hub struct {
	base     string
	token    string // sent only to base; never printed
	client   *http.Client
	interval time.Duration
	last     time.Time
	infos    map[string]*modelInfo
}

func newHub(base, token string, interval time.Duration) *hub {
	return &hub{
		base:     strings.TrimSuffix(base, "/"),
		token:    token,
		client:   &http.Client{Timeout: 2 * time.Minute},
		interval: interval,
		infos:    map[string]*modelInfo{},
	}
}

// info returns a repo's listing at its current commit, asking once per run.
func (h *hub) info(ctx context.Context, repo string) (*modelInfo, error) {
	if m, ok := h.infos[repo]; ok {
		return m, nil
	}
	body, err := h.get(ctx, "/api/models/"+repo+"?blobs=true")
	if err != nil {
		return nil, err
	}
	m := &modelInfo{}
	if err := json.Unmarshal(body, m); err != nil {
		return nil, fmt.Errorf("reading the listing of %s: %w", repo, err)
	}
	if m.ID != repo {
		return nil, fmt.Errorf("%s moved to %s on Hugging Face; use the new ID", repo, m.ID)
	}
	if !revisionPattern.MatchString(m.SHA) {
		return nil, fmt.Errorf("%s has the revision %q on Hugging Face, not a commit SHA", repo, m.SHA)
	}
	h.infos[repo] = m
	return m, nil
}

// config returns config.json of repo at revision.
func (h *hub) config(ctx context.Context, repo, revision string) (map[string]any, error) {
	body, err := h.get(ctx, "/"+repo+"/resolve/"+revision+"/config.json")
	if err != nil {
		return nil, err
	}
	var cfg map[string]any
	if err := json.Unmarshal(body, &cfg); err != nil {
		return nil, fmt.Errorf("reading config.json of %s: %w", repo, err)
	}
	return cfg, nil
}

func (h *hub) get(ctx context.Context, path string) ([]byte, error) {
	const attempts = 5
	for attempt := 1; ; attempt++ {
		if err := h.wait(ctx, h.interval-time.Since(h.last)); err != nil {
			return nil, err
		}
		h.last = time.Now()
		body, retryAfter, err := h.once(ctx, path)
		if retryAfter == 0 || attempt == attempts {
			return body, err
		}
		if err := h.wait(ctx, retryAfter); err != nil {
			return nil, err
		}
	}
}

// once makes one request. A non-zero retryAfter means try again after it.
func (h *hub) once(ctx context.Context, path string) (body []byte, retryAfter time.Duration, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.base+path, http.NoBody)
	if err != nil {
		return nil, 0, err
	}
	if h.token != "" {
		// On a redirect, net/http forwards this header only to the same
		// host or one of its subdomains (cdn-lfs.huggingface.co, say) and
		// drops it anywhere else.
		req.Header.Set("Authorization", "Bearer "+h.token)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, 5 * time.Second, fmt.Errorf("GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, err = io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, 5 * time.Second, fmt.Errorf("GET %s: %w", path, err)
	}
	switch {
	case resp.StatusCode == http.StatusOK:
		return body, 0, nil
	case resp.Header.Get("X-Error-Code") == hubRepoNotFound:
		return nil, 0, fmt.Errorf("GET %s: %w", path, errRepoNotFound)
	case resp.Header.Get("X-Error-Code") == hubGatedRepo,
		resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, 0, fmt.Errorf("GET %s: %w", path, errNeedsToken)
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, retryDelay(resp.Header.Get("Retry-After")), fmt.Errorf("GET %s: Hugging Face is rate limiting this client", path)
	case resp.StatusCode >= 500:
		return nil, 5 * time.Second, fmt.Errorf("GET %s: %s", path, resp.Status)
	}
	return nil, 0, fmt.Errorf("GET %s: %s", path, resp.Status)
}

// retryDelay reads Retry-After in seconds, defaulting to a minute.
func retryDelay(header string) time.Duration {
	if s, err := strconv.Atoi(strings.TrimSpace(header)); err == nil && s > 0 {
		return time.Duration(s) * time.Second
	}
	return time.Minute
}

func (h *hub) wait(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
