package plane

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// requestContext separates request signatures from every other signature
// made with the same key. It is prepended to the message before signing.
const requestContext = "loomplane/dbgateway/request/v1\x00"

const (
	headerGateway   = "Loomplane-Gateway"
	headerTimestamp = "Loomplane-Timestamp"
	headerSignature = "Loomplane-Signature"
)

const (
	// pollTimeout bounds one snapshot poll: the control plane holds it for
	// up to 25s, and the rest is slack for the network.
	pollTimeout = 40 * time.Second
	// maxSnapshotBody and maxReplyBody cap what the gateway reads from the
	// control plane, so a wrong answer can't fill its memory.
	maxSnapshotBody = 64 << 20
	maxReplyBody    = 1 << 20
)

// SignRequest signs req for the control plane and sets its Loomplane-Gateway,
// Loomplane-Timestamp (unix seconds) and Loomplane-Signature (base64) headers.
// The signature is Ed25519 over the request context, the method, the request
// URI, the timestamp and the hex SHA-256 of body, with a newline between each
// of the last four. The request URI is the escaped path plus "?" and the raw
// query when there is one, which is what a server reads as r.RequestURI, so
// the query is covered too. body is the bytes the request will send, or nil.
func SignRequest(req *http.Request, body []byte, id string, key ed25519.PrivateKey, now time.Time) {
	method := req.Method
	if method == "" {
		method = http.MethodGet
	}
	ts := strconv.FormatInt(now.Unix(), 10)
	sum := sha256.Sum256(body)
	msg := requestContext + method + "\n" + req.URL.RequestURI() + "\n" + ts + "\n" + hex.EncodeToString(sum[:])
	req.Header.Set(headerGateway, id)
	req.Header.Set(headerTimestamp, ts)
	req.Header.Set(headerSignature, base64.StdEncoding.EncodeToString(ed25519.Sign(key, []byte(msg))))
}

// Report is what an instance tells the control plane every few seconds.
type Report struct {
	InstanceID      string       `json:"instance_id"`
	SnapshotVersion uint64       `json:"snapshot_version"`
	Draining        bool         `json:"draining"`
	Routes          []RouteStats `json:"routes"`
}

// ReportResponse is the control plane's answer to a Report: how many
// instances of the gateway reported lately, this one included.
type ReportResponse struct {
	LiveInstances int `json:"live_instances"`
}

// ControlClient talks to the Loomplane control plane as one gateway. Every
// request is signed with the gateway's key. It never follows a redirect, so a
// signed request goes only where the control URL says.
type ControlClient struct {
	base string
	id   string
	key  ed25519.PrivateKey
	hc   *http.Client
	now  func() time.Time
}

var _ Holder = (*ControlClient)(nil)

// NewControlClient returns a client for the control plane at baseURL, acting
// as gateway id. A trailing slash on baseURL is ignored.
func NewControlClient(baseURL, id string, key ed25519.PrivateKey) *ControlClient {
	return &ControlClient{
		base: strings.TrimRight(baseURL, "/"),
		id:   id,
		key:  key,
		hc: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
		now: time.Now,
	}
}

// do sends one signed request to /api/v1/db-gateways/{id}/<leaf> and returns
// the status and the body, read up to limit bytes.
func (c *ControlClient) do(ctx context.Context, method, leaf, query string, body []byte, limit int64) (int, []byte, error) {
	u := c.base + "/api/v1/db-gateways/" + url.PathEscape(c.id) + "/" + leaf
	if query != "" {
		u += "?" + query
	}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	SignRequest(req, body, c.id, c.key, c.now())
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	reply, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return 0, nil, err
	}
	if int64(len(reply)) > limit {
		return 0, nil, fmt.Errorf("control plane %s: reply is larger than %d bytes", leaf, limit)
	}
	return resp.StatusCode, reply, nil
}

// Poll waits for a snapshot newer than after. The control plane holds the
// request for up to 25s; a nil snapshot and a nil error mean it had nothing
// new. Poll doesn't verify what it returns.
func (c *ControlClient) Poll(ctx context.Context, after uint64) (*Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, pollTimeout)
	defer cancel()
	status, reply, err := c.do(ctx, http.MethodGet, "snapshot", "after="+strconv.FormatUint(after, 10), nil, maxSnapshotBody)
	if err != nil {
		return nil, err
	}
	switch status {
	case http.StatusNoContent:
		return nil, nil
	case http.StatusOK:
		var s Snapshot
		if err := json.Unmarshal(reply, &s); err != nil {
			return nil, errors.New("control plane snapshot: reply is not a snapshot")
		}
		return &s, nil
	}
	return nil, fmt.Errorf("control plane snapshot: status %d", status)
}

// Report sends r and returns the control plane's answer.
func (c *ControlClient) Report(ctx context.Context, r Report) (ReportResponse, error) {
	if r.Routes == nil {
		r.Routes = []RouteStats{}
	}
	body, err := json.Marshal(r)
	if err != nil {
		return ReportResponse{}, err
	}
	status, reply, err := c.do(ctx, http.MethodPost, "report", "", body, maxReplyBody)
	if err != nil {
		return ReportResponse{}, err
	}
	if status < 200 || status > 299 {
		return ReportResponse{}, fmt.Errorf("control plane report: status %d", status)
	}
	var out ReportResponse
	if err := json.Unmarshal(reply, &out); err != nil {
		return ReportResponse{}, errors.New("control plane report: reply is not a report response")
	}
	return out, nil
}

// Hold asks the control plane to bring up the route for l, such as a branch
// created on first connect. Any answer outside 2xx is an error, a 404
// included: it means the control plane has no hold handler.
func (c *ControlClient) Hold(ctx context.Context, l Label) error {
	body, err := json.Marshal(struct {
		Label string `json:"label"`
	}{l.String()})
	if err != nil {
		return err
	}
	status, _, err := c.do(ctx, http.MethodPost, "hold", "", body, maxReplyBody)
	if err != nil {
		return err
	}
	if status < 200 || status > 299 {
		return fmt.Errorf("control plane hold: status %d", status)
	}
	return nil
}
