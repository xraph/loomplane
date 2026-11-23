package aws

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestRegistryPolicyReachesOnlyItsPrefix(t *testing.T) {
	s := &Session{region: "us-east-1", account: "123456789012"}
	for _, push := range []bool{false, true} {
		raw, err := s.RegistryPolicy("acme/loomplane", push)
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Statement []struct {
				Action   any `json:"Action"`
				Resource any `json:"Resource"`
			} `json:"Statement"`
		}
		if err = json.Unmarshal([]byte(raw), &doc); err != nil {
			t.Fatal(err)
		}
		if len(doc.Statement) != 2 {
			t.Fatalf("statements %+v", doc.Statement)
		}
		resources := doc.Statement[1].Resource.([]any)
		want := "arn:aws:ecr:us-east-1:123456789012:repository/acme/loomplane"
		if len(resources) != 2 || resources[0] != want || resources[1] != want+"/*" {
			t.Fatalf("resources %v", resources)
		}
		list := doc.Statement[1].Action.([]any)
		actions := make([]string, 0, len(list))
		for _, a := range list {
			actions = append(actions, a.(string))
		}
		if slices.Contains(actions, "ecr:PutImage") != push {
			t.Fatalf("push=%v but actions %v", push, actions)
		}
		if slices.Contains(actions, "ecr:*") || slices.Contains(actions, "ecr:DeleteRepository") {
			t.Fatalf("the policy grants too much: %v", actions)
		}
	}
	for _, bad := range []string{"", "/acme", "acme/", "Acme", "acme/../x", "*"} {
		if _, err := s.RegistryPolicy(bad, true); err == nil {
			t.Errorf("prefix %q was accepted", bad)
		}
	}
	if got := s.RegistryEndpoint(); got != "https://123456789012.dkr.ecr.us-east-1.amazonaws.com" {
		t.Fatalf("endpoint %s", got)
	}
}
