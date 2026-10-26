// Package keyless describes a connection that holds no cloud secret. The
// cloud trusts Loomplane as an OpenID Connect issuer, and what Loomplane
// keeps for the connection is a handout: who it is to the cloud, the
// audience the cloud expects, and a ticket only the issuer can make. A
// cloud client hands it to a Minter for a fresh token each time it needs
// one, and trades that token for the cloud's own short-lived credentials.
//
// It imports nothing from Loomplane, so every cloud package can use it.
package keyless

import (
	"context"
	"encoding/json"
	"errors"
)

// Type is the "type" a handout's JSON carries, so it's told apart from a
// cloud's own credential JSON, such as a Google service account key.
const Type = "loomplane_keyless"

// Handout is what a keyless connection keeps instead of a credential.
// Subject is the connection's identity to the cloud and Audience is what
// the cloud's trust expects in a token's aud. Ticket is the issuer's HMAC
// over both, so a handout made anywhere else can't get a token. Settings
// carry what the cloud needs to trade a token, such as a role to assume.
//
// The ticket is a secret: it never goes in an error, a log or a response.
type Handout struct {
	Type     string            `json:"type"`
	Subject  string            `json:"subject"`
	Audience string            `json:"audience"`
	Ticket   string            `json:"ticket"`
	Settings map[string]string `json:"settings"`
}

// Minter makes a short-lived signed token for a handout.
type Minter interface {
	Mint(ctx context.Context, h Handout) (string, error)
}

// ErrNoMinter is what a cloud client returns for a handout when it was
// made without a Minter.
var ErrNoMinter = errors.New("keyless isn't available in this process")

// Parse reads data as a handout. ok is false for anything whose JSON type
// isn't Type, including data that isn't JSON, so a caller goes on to treat
// it as a credential of its own. ok is true with an error when the type
// matches but the handout is unusable: a field of the wrong shape, or an
// empty subject, audience or ticket.
func Parse(data []byte) (h Handout, ok bool, err error) {
	var probe struct {
		Type any `json:"type"`
	}
	if json.Unmarshal(data, &probe) != nil || probe.Type != Type {
		return Handout{}, false, nil
	}
	if err = json.Unmarshal(data, &h); err != nil {
		// json's errors can quote the input, and the input holds the
		// ticket, so they're not passed on.
		return Handout{}, true, errors.New("keyless handout isn't valid JSON")
	}
	switch {
	case h.Subject == "":
		return Handout{}, true, errors.New("keyless handout has no subject")
	case h.Audience == "":
		return Handout{}, true, errors.New("keyless handout has no audience")
	case h.Ticket == "":
		return Handout{}, true, errors.New("keyless handout has no ticket")
	}
	return h, true, nil
}

// Subject is a connection's identity to its cloud. Each cloud's trust
// matches it exactly, so it names the organization and the connection.
func Subject(orgID, connectionID string) string {
	return "org:" + orgID + ":connection:" + connectionID
}

// Step is one step of setting up a cloud's trust that the dialog shows: a
// title, what to do, and a command to copy when there's one. It lives here
// so the cloud packages can return it without importing connections.
type Step struct {
	Title   string `json:"title"`
	Body    string `json:"body,omitempty"`
	Command string `json:"command,omitempty"`
}
