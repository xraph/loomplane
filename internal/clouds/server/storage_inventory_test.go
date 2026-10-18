package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/xraph/ctrlplane/id"

	"github.com/xraph/loomplane/internal/clouds/owned"
)

func TestStorageInventoryScopesVolumesAndChecksEveryContainer(t *testing.T) {
	iid := id.New(id.PrefixInstance)
	attached := VolumeName(iid, "data")
	detached := VolumeName(iid, "archive")
	foreign := VolumeName(iid, "foreign")
	labels := func(role, org string) map[string]string {
		return map[string]string{owned.LabelOrg: org, owned.LabelConnection: "connection", owned.LabelProject: "project", owned.LabelEnvironment: "env", owned.LabelInstance: iid.String(), owned.LabelVolumeRole: role}
	}
	c := &Client{Config: &Config{Details: map[string]string{"docker_id": "daemon"}}, HTTP: &http.Client{Transport: runtimeTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet {
			t.Fatalf("inventory mutated Docker: %s", r.Method)
		}
		var body any
		switch r.URL.Path {
		case "/info":
			body = map[string]any{"ID": "daemon"}
		case "/volumes":
			body = map[string]any{"Volumes": []any{
				map[string]any{"Name": attached, "Driver": "local", "Labels": labels("data", "org")},
				map[string]any{"Name": detached, "Driver": "local", "Labels": labels("archive", "org")},
				map[string]any{"Name": foreign, "Driver": "local", "Labels": labels("foreign", "another-org")},
			}}
		case "/containers/json":
			if r.URL.Query().Get("all") != "true" {
				t.Error("stopped containers were omitted")
			}
			body = []any{map[string]any{"Labels": map[string]string{owned.LabelOrg: "another-org"}, "Mounts": []any{map[string]any{"Type": "volume", "Name": attached}}}}
		default:
			t.Fatalf("unexpected inventory request %s", r.URL)
		}
		return jsonResponse(body), nil
	})}}
	volumes, err := c.StorageVolumes(t.Context(), "org", "connection")
	if err != nil || len(volumes) != 2 {
		t.Fatalf("scoped volumes: %+v %v", volumes, err)
	}
	byName := map[string]owned.StoredVolume{}
	for _, volume := range volumes {
		byName[volume.Name] = volume
	}
	if byName[attached].Status != "in-use" || byName[detached].Status != "retained" || byName[attached].CapacityBytes != 0 || byName[attached].ProjectID != "project" {
		t.Fatalf("incorrect inventory: %+v", volumes)
	}
	data, _ := json.Marshal(volumes)
	if string(data) == "null" {
		t.Fatal("missing inventory")
	}
}

func jsonResponse(body any) *http.Response {
	data, _ := json.Marshal(body)
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(data))}
}

func TestStorageInventoryRefusesChangedDaemonAndUnreadableAttachments(t *testing.T) {
	for _, changed := range []bool{false, true} {
		c := &Client{Config: &Config{Details: map[string]string{"docker_id": "reviewed"}}, HTTP: &http.Client{Transport: runtimeTransport(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path == "/info" {
				identity := "reviewed"
				if changed {
					identity = "changed"
				}
				return jsonResponse(map[string]string{"ID": identity}), nil
			}
			if r.URL.Path == "/volumes" {
				return jsonResponse(map[string]any{"Volumes": []any{}}), nil
			}
			return nil, errors.New("attachment inspection unavailable")
		})}}
		if _, err := c.StorageVolumes(t.Context(), "org", "connection"); err == nil {
			t.Fatalf("incomplete inventory accepted, changed=%v", changed)
		}
	}
}
