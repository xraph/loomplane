package client

import (
	"encoding/json"
	"testing"
)

func TestNativeReviewRoundTripKeepsRDSAccountAndNetwork(t *testing.T) {
	input := []byte(`{"target":{"provider":"rds","native_account":"123456789012","rds_network":{"vpc_id":"vpc-private","subnet_group":"recovery","security_group_id":"sg-private","verifier_group_id":"sg-worker"}}}`)
	var review NativeRecoveryReview
	if err := json.Unmarshal(input, &review); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(review)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Target map[string]json.RawMessage `json:"target"`
	}
	if err = json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if string(out.Target["native_account"]) != `"123456789012"` || len(out.Target["rds_network"]) == 0 {
		t.Fatal("saved native review dropped its RDS account or private network", string(raw))
	}
	var network map[string]json.RawMessage
	if err = json.Unmarshal(out.Target["rds_network"], &network); err != nil {
		t.Fatal(err)
	}
	if string(network["vpc_id"]) != `"vpc-private"` || string(network["security_group_id"]) != `"sg-private"` {
		t.Fatal("saved native route changed", network)
	}
	raw, err = json.Marshal(NativeRecoveryTarget{Provider: "cloud-sql"})
	if err != nil {
		t.Fatal(err)
	}
	out.Target = nil
	if err = json.Unmarshal(raw, &out.Target); err != nil {
		t.Fatal(err)
	}
	if _, present := out.Target["rds_network"]; present {
		t.Fatal("legacy Cloud SQL target gained an empty network")
	}
}
