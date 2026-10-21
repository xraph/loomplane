package client

import (
	"encoding/json"
	"testing"
)

func TestPlanRoundTripKeepsReviewedResourceSettings(t *testing.T) {
	const raw = `{"actions":[{"id":"resource.database","resource":{"provider":"rds","kind":"postgres","managed":{"profile":{"id":"postgresql-17-rds","revision":"1","storage_gb":20},"connection_id":"selected","account_identity":"reviewed"}}}]}`
	var plan Plan
	if err := json.Unmarshal([]byte(raw), &plan); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Actions []struct {
			Resource struct {
				Managed struct {
					Profile struct {
						ID        string
						StorageGB int `json:"storage_gb"`
					}
					ConnectionID    string `json:"connection_id"`
					AccountIdentity string `json:"account_identity"`
				}
			}
		}
	}
	if err := json.Unmarshal(encoded, &body); err != nil {
		t.Fatal(err)
	}
	managed := body.Actions[0].Resource.Managed
	if managed.Profile.ID != "postgresql-17-rds" || managed.Profile.StorageGB != 20 || managed.ConnectionID != "selected" || managed.AccountIdentity != "reviewed" {
		t.Fatal("CLI plan output discarded the resource settings the user must review")
	}
}
