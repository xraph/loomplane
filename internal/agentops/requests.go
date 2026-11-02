package agentops

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// QuestionTTL is how long a run waits for a person to answer one of its
// requests before cortex gives up on it.
const QuestionTTL = 24 * time.Hour

// requestOps are the requests to a person. Cortex pauses the run on each
// as an external tool; agentrequests asks the person and hands the answer
// back.
var requestOps = []string{"ask_person", "request_secret", "request_github_connection", "request_cloud_connection"}

func registerRequests(r *Registry) {
	for _, name := range requestOps {
		RegisterExternal(r, def(name), QuestionTTL)
	}
}

// ErrTeamWithoutReason is a request that names a team and doesn't say why.
// The model reads it and can ask again.
var ErrTeamWithoutReason = errors.New("say why this team: a request that names a team needs team_reason")

// ValidateRequest checks a request to a person before cortex suspends the
// run on it. A team needs a reason that isn't blank; whether the team
// exists is decided when the request is routed, which falls back to the
// workspace's admins.
func ValidateRequest(name string, args json.RawMessage) error {
	if !slices.Contains(requestOps, name) {
		return fmt.Errorf("%s isn't a request to a person", name)
	}
	var in struct {
		Team       string `json:"team"`
		TeamReason string `json:"team_reason"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &in); err != nil {
			return fmt.Errorf("invalid arguments: %w", err)
		}
	}
	if strings.TrimSpace(in.Team) != "" && strings.TrimSpace(in.TeamReason) == "" {
		return ErrTeamWithoutReason
	}
	return nil
}
