// Package realtime exposes deployment events through Forge streams.
package realtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/xraph/loomplane/internal/security"
)

const maxSubscriptionBytes = 16 << 10

var ErrInvalid = errors.New("invalid realtime subscription")
var scopeID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// Subscription describes desired scope, never an authentication credential.
type Subscription struct {
	Op            string   `json:"op"`
	OrgID         string   `json:"org_id"`
	ProjectIDs    []string `json:"project_ids,omitempty"`
	DeploymentIDs []string `json:"deployment_ids,omitempty"`
	Cursor        string   `json:"cursor,omitempty"`
}

func parseSubscription(p security.Principal, raw []byte) (Subscription, error) {
	var out Subscription
	if len(raw) > maxSubscriptionBytes {
		return out, ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&out); err != nil {
		return out, ErrInvalid
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return out, ErrInvalid
	}
	if out.OrgID != p.OrgID {
		return out, security.ErrOrgMismatch
	}
	if out.Op != "subscribe" || len(out.ProjectIDs) > 64 || len(out.DeploymentIDs) > 64 {
		return out, ErrInvalid
	}
	if out.Cursor != "" {
		org, seq, ok := strings.Cut(out.Cursor, ":")
		if !ok || org != p.OrgID {
			return out, ErrInvalid
		}
		value, err := strconv.ParseInt(seq, 10, 64)
		if err != nil || value < 0 {
			return out, ErrInvalid
		}
	}
	for _, ids := range [][]string{out.ProjectIDs, out.DeploymentIDs} {
		for _, id := range ids {
			if !scopeID.MatchString(id) {
				return out, ErrInvalid
			}
		}
	}
	slices.Sort(out.ProjectIDs)
	out.ProjectIDs = slices.Compact(out.ProjectIDs)
	slices.Sort(out.DeploymentIDs)
	out.DeploymentIDs = slices.Compact(out.DeploymentIDs)
	return out, nil
}
