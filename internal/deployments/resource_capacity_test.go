package deployments

import (
	"encoding/json"
	"testing"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
)

func TestReviewedResizeRequiresAnOwnedBindingOnTheReviewedAccount(t *testing.T) {
	old := planning.ResourceConfig{Provider: "digitalocean", Kind: "postgres", Region: "nyc"}
	before, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	next := old
	job := Job{Connection: &connections.Connection{ID: "account"}, Action: planning.Action{Op: planning.Update, Resource: &next}, Binding: &planning.Binding{ConnectionID: "account", Config: before}}
	next.Settings = map[string]string{"nodes": "1"}
	if resize, e := reviewedResize(&job); e != nil || resize {
		t.Fatalf("legacy default triggered resize: %v %v", resize, e)
	}
	next.Settings["nodes"] = "2"
	if resize, e := reviewedResize(&job); e != nil || !resize {
		t.Fatalf("reviewed resize: %v %v", resize, e)
	}
	job.Action.ConnectionID = "other"
	if _, e := reviewedResize(&job); e == nil {
		t.Fatal("account mismatch accepted")
	}
	job.Action.ConnectionID = "account"
	next.ExistingResource = &planning.ExistingResourceConfig{}
	if _, e := reviewedResize(&job); e == nil {
		t.Fatal("attached database accepted for resize")
	}
}
