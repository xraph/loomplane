package kubernetes

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/xraph/loomplane/internal/config"
)

func TestReplicaProofBindsAcceptedSpec(t *testing.T) {
	key := bytes.Repeat([]byte{11}, 32)
	scope := ManagedScope{Org: "org", NamespaceUID: "ns", Release: "release", Epoch: 4}
	base := map[string]any{"metadata": map[string]any{"labels": map[string]string{"owner": "one"}}, "spec": map[string]any{"replicas": 1, "selector": map[string]string{"app": "one"}, "strategy": "RollingUpdate", "template": map[string]any{"env": "private", "security": "restricted", "image": "pinned", "resources": "500m", "nodeSelector": "amd64"}}}
	encode := func(v any) []byte {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	body := encode(base)
	mac := replicaSpecMAC(key, scope, "deployments", "uid", body)
	if len(mac) != 32 || bytes.Contains(mac, []byte("private")) {
		t.Fatal("invalid private comparison identity")
	}
	spec := base["spec"].(map[string]any)
	spec["replicas"] = 3
	if !bytes.Equal(mac, replicaSpecMAC(key, scope, "deployments", "uid", encode(base))) {
		t.Fatal("replicas included")
	}
	for _, field := range []string{"selector", "strategy", "template"} {
		prev := spec[field]
		spec[field] = "changed"
		if bytes.Equal(mac, replicaSpecMAC(key, scope, "deployments", "uid", encode(base))) {
			t.Fatal(field)
		}
		spec[field] = prev
	}
	for field, prev := range spec["template"].(map[string]any) {
		spec["template"].(map[string]any)[field] = "changed"
		if bytes.Equal(mac, replicaSpecMAC(key, scope, "deployments", "uid", encode(base))) {
			t.Fatal(field)
		}
		spec["template"].(map[string]any)[field] = prev
	}
	other := scope
	other.Epoch++
	for _, otherMAC := range [][]byte{replicaSpecMAC(nil, scope, "deployments", "uid", body), replicaSpecMAC(bytes.Repeat([]byte{12}, 32), scope, "deployments", "uid", body), replicaSpecMAC(key, other, "deployments", "uid", body), replicaSpecMAC(key, scope, "horizontalpodautoscalers", "uid", body), replicaSpecMAC(key, scope, "deployments", "other", body)} {
		if bytes.Equal(mac, otherMAC) {
			t.Fatal("cross-context proof")
		}
	}
}

func TestReplicaProofNeedsPrivateKey(t *testing.T) {
	for _, secret := range []string{"", "broken", base64.StdEncoding.EncodeToString([]byte("short")), config.Defaults().SecretKey, config.Defaults().SecretKey + "\n"} {
		key, err := ReplicaProofKey(secret)
		if err != nil || len(key) != 0 {
			t.Fatal("unprotected proof key")
		}
	}
	configured := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{19}, 32))
	key, err := ReplicaProofKey(configured)
	if err != nil || len(key) != 32 {
		t.Fatal("private proof unavailable")
	}
	after, err := ReplicaProofKey(configured)
	if err != nil || !bytes.Equal(key, after) {
		t.Fatal("unstable key")
	}
}
