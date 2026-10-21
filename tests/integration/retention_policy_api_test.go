//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/recovery"
)

// Reuse the existing authenticated recovery fixture after its explicit deletions
// finish, so enabling a policy cannot race those independent requests.
func verifyRetentionPolicyControls(t *testing.T, owner, reader *client, runCLI func(string, ...string) ([]byte, error), writeKey, readKey string) {
	t.Helper()
	path := "/api/v1/service-backups/witness/retention-policy"
	var state recovery.RetentionPolicyState
	owner.must(http.StatusOK, http.MethodGet, path, nil, &state)
	if state.Configured || state.Policy != nil {
		t.Fatalf("unset policy: %+v", state)
	}
	var history recovery.RetentionPolicyHistory
	reader.must(http.StatusOK, http.MethodGet, path+"/history", nil, &history)
	if history.Events == nil || len(history.Events) != 0 {
		t.Fatalf("unset history: %+v", history)
	}
	in := recovery.RetentionPolicyInput{Policy: recovery.RetentionInput{OlderThanDays: 7, KeepLast: 1}}
	reader.must(http.StatusForbidden, http.MethodPut, path, in, nil)
	for _, invalid := range []recovery.RetentionPolicyInput{{Revision: -1, Policy: in.Policy}, {Policy: recovery.RetentionInput{OlderThanDays: 7, KeepLast: 0}}} {
		owner.must(http.StatusUnprocessableEntity, http.MethodPut, path, invalid, nil)
	}
	var saved recovery.RetentionPolicy
	owner.must(http.StatusOK, http.MethodPut, path, in, &saved)
	if saved.Revision != 1 || saved.Enabled || saved.AuthorizedBy == "" {
		t.Fatalf("disabled policy creation: %+v", saved)
	}
	originalID, creator := saved.ID, saved.CreatedBy
	in.Revision, in.Enabled = 1, true
	owner.must(http.StatusOK, http.MethodPut, path, in, &saved)
	if saved.ID != originalID || saved.Revision != 2 || !saved.Enabled {
		t.Fatalf("explicit enable: %+v", saved)
	}
	owner.must(http.StatusConflict, http.MethodPut, path, in, nil)
	in.Revision, in.Policy = 2, recovery.RetentionInput{OlderThanDays: 30, KeepLast: 2}
	owner.must(http.StatusOK, http.MethodPut, path, in, &saved)
	if saved.Revision != 3 || saved.Policy != in.Policy || saved.CreatedBy != creator {
		t.Fatalf("policy change: %+v", saved)
	}
	output, err := runCLI(writeKey, "backups", "retention-pause", "witness", "--yes", "--json")
	if err != nil || json.Unmarshal(output, &saved) != nil || saved.Revision != 4 || saved.Enabled || saved.Policy != in.Policy {
		t.Fatalf("CLI pause: %v %s", err, output)
	}
	reader.must(http.StatusOK, http.MethodGet, "/api/v1/service-backups/candidate-a/retention-policy", nil, &state)
	if !state.Configured || state.Policy == nil || state.Policy.ID != originalID || state.Policy.Revision != 4 || state.Policy.Enabled {
		t.Fatalf("shared source state: %+v", state)
	}
	raw := reader.raw(path)
	for _, private := range []string{`"source_key"`, `"object"`, `"request_id"`, `"token"`} {
		if strings.Contains(raw, private) {
			t.Fatalf("policy exposed %s", private)
		}
	}
	reader.must(http.StatusOK, http.MethodGet, path+"/history?limit=2", nil, &history)
	if len(history.Events) != 2 || history.Events[0].Revision != 4 || history.Events[0].Enabled || history.Events[1].Revision != 3 || history.NextBefore != "3" {
		t.Fatalf("history first page: %+v", history)
	}
	history = recovery.RetentionPolicyHistory{}
	reader.must(http.StatusOK, http.MethodGet, path+"/history?limit=2&before=3", nil, &history)
	if len(history.Events) != 2 || history.Events[0].Revision != 2 || !history.Events[0].Enabled || history.Events[1].Revision != 1 || history.NextBefore != "" {
		t.Fatalf("history continuation: %+v", history)
	}
	for _, event := range history.Events {
		if event.AuthorizedBy != creator || event.Policy.OlderThanDays != 7 {
			t.Fatalf("history changed: %+v", event)
		}
	}
	for _, query := range []string{"limit=101", "before=0", "before=oops", "before=01"} {
		reader.must(http.StatusUnprocessableEntity, http.MethodGet, path+"/history?"+query, nil, nil)
	}
	reader.must(http.StatusBadRequest, http.MethodGet, path+"/history?limit=oops", nil, nil)
	output, err = runCLI(readKey, "backups", "retention-policy", "witness", "--json")
	if err != nil || json.Unmarshal(output, &state) != nil || !state.Configured || state.Policy == nil || state.Policy.Revision != 4 {
		t.Fatalf("reader CLI state: %v %s", err, output)
	}
	output, err = runCLI(readKey, "backups", "retention-history", "witness", "--limit", "1", "--json")
	if err != nil || json.Unmarshal(output, &history) != nil || len(history.Events) != 1 || history.Events[0].Revision != 4 {
		t.Fatalf("reader CLI history: %v %s", err, output)
	}
}
