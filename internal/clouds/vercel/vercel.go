// Package vercel talks to Vercel's REST API for Loomplane: it checks an
// access token and, later, creates deployments with it.
package vercel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultEndpoint is Vercel's API.
const DefaultEndpoint = "https://api.vercel.com"

const (
	requestTimeout = 60 * time.Second
	maxResponse    = 4 << 20
	maxToken       = 512
)

// Errors.
var (
	// ErrInvalidToken is something that can't be a Vercel token.
	ErrInvalidToken = errors.New("that isn't a Vercel access token")
	// ErrRejected is a token Vercel refused: expired, revoked or without
	// access to the team.
	ErrRejected = errors.New("vercel refused the access token")
)

// Client calls Vercel's API.
type Client struct {
	endpoint string
	http     *http.Client
}

// New returns a Client. An empty endpoint means Vercel's own.
func New(endpoint string) *Client {
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	return &Client{endpoint: strings.TrimRight(endpoint, "/"), http: &http.Client{Timeout: requestTimeout}}
}

// Token is an access token, and the team it acts for. TeamID is empty
// for a personal account.
type Token struct {
	Value  string
	TeamID string
}

// CheckToken tells whether s could be a token, without asking Vercel.
func CheckToken(s string) error {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > maxToken || strings.ContainsAny(s, " \t\r\n") {
		return ErrInvalidToken
	}
	return nil
}

// APIError is an error response from Vercel.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("vercel answered %d: %s", e.Status, e.Message)
}

// IsNotFound reports whether err is Vercel's 404.
func IsNotFound(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == http.StatusNotFound
}

// do sends a request for tok, scoped to its team, and decodes the JSON
// response into out.
func (c *Client) do(ctx context.Context, tok Token, method, path string, query url.Values, body io.Reader, contentType string, out any) error {
	if query == nil {
		query = url.Values{}
	}
	if tok.TeamID != "" {
		query.Set("teamId", tok.TeamID)
	}
	u := c.endpoint + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(tok.Value))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("vercel: %w", err)
	}
	defer res.Body.Close() // read-only; errors surface through ReadAll
	data, err := io.ReadAll(io.LimitReader(res.Body, maxResponse))
	if err != nil {
		return fmt.Errorf("vercel: read response: %w", err)
	}
	if res.StatusCode >= 300 {
		var e struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		ae := &APIError{Status: res.StatusCode, Message: strings.TrimSpace(string(data))}
		if json.Unmarshal(data, &e) == nil && e.Error.Message != "" {
			ae.Code, ae.Message = e.Error.Code, e.Error.Message
		}
		if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
			return fmt.Errorf("%w: %s", ErrRejected, ae.Message)
		}
		return ae
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("vercel: decode response: %w", err)
	}
	return nil
}

func (c *Client) json(ctx context.Context, tok Token, method, path string, query url.Values, in, out any) error {
	var body io.Reader
	ct := ""
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		body, ct = bytes.NewReader(data), "application/json"
	}
	return c.do(ctx, tok, method, path, query, body, ct, out)
}

// Account is who a token deploys as: a team, or a person's own account.
type Account struct {
	// Name is the team's slug, or the person's username.
	Name string
	// TeamID is the team's ID, empty for a personal account.
	TeamID string
}

// Check asks Vercel who tok is, and whether it can reach its team.
func (c *Client) Check(ctx context.Context, tok Token) (Account, error) {
	if tok.TeamID != "" {
		var team struct {
			ID   string `json:"id"`
			Slug string `json:"slug"`
		}
		// The team is in the path, so it isn't sent as teamId too.
		if err := c.json(ctx, Token{Value: tok.Value}, http.MethodGet, "/v2/teams/"+url.PathEscape(tok.TeamID), nil, nil, &team); err != nil {
			if IsNotFound(err) {
				return Account{}, fmt.Errorf("%w: it can't see team %s", ErrRejected, tok.TeamID)
			}
			return Account{}, err
		}
		return Account{Name: team.Slug, TeamID: team.ID}, nil
	}
	var me struct {
		User struct {
			Username string `json:"username"`
		} `json:"user"`
	}
	if err := c.json(ctx, tok, http.MethodGet, "/v2/user", nil, nil, &me); err != nil {
		return Account{}, err
	}
	return Account{Name: me.User.Username}, nil
}
