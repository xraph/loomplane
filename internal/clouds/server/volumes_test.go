package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xraph/ctrlplane/id"
	cpprovider "github.com/xraph/ctrlplane/provider"

	"github.com/xraph/loomplane/internal/clouds/owned"
)

func TestPersistentVolumesReuseOnlyMatchingOwnership(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		t.Run(map[bool]string{false: "retained", true: "foreign"}[foreign], func(t *testing.T) {
			iid := id.New(id.PrefixInstance)
			labels := map[string]string{owned.LabelOrg: "org", owned.LabelProject: "project", owned.LabelEnvironment: "env", owned.LabelConnection: "target"}
			spec := []cpprovider.VolumeSpec{{Name: "data", MountPath: "/var/lib/postgresql/data", Type: "persistent"}}
			var saved map[string]any
			creates := 0
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method {
				case http.MethodGet:
					if saved == nil {
						http.NotFound(w, r)
						return
					}
					_ = json.NewEncoder(w).Encode(saved)
				case http.MethodPost:
					creates++
					if err := json.NewDecoder(r.Body).Decode(&saved); err != nil {
						t.Error(err)
					}
					_ = json.NewEncoder(w).Encode(saved)
				default:
					t.Errorf("unexpected volume mutation %s", r.Method)
				}
			}))
			defer api.Close()
			c := &Client{HTTP: &http.Client{Transport: runtimeTransport(func(r *http.Request) (*http.Response, error) {
				req, err := http.NewRequestWithContext(r.Context(), r.Method, api.URL+r.URL.RequestURI(), r.Body)
				if err != nil {
					return nil, err
				}
				return http.DefaultTransport.RoundTrip(req)
			})}}
			mounts, err := ensureVolumes(t.Context(), c, iid, spec, labels)
			if err != nil || len(mounts) != 1 {
				t.Fatalf("create: %v %v", mounts, err)
			}
			if foreign {
				saved["Labels"].(map[string]any)[owned.LabelConnection] = "other-target"
			}
			again, err := ensureVolumes(t.Context(), c, iid, spec, labels)
			if foreign {
				if !errors.Is(err, ErrOwned) {
					t.Fatalf("foreign volume accepted: %v", err)
				}
			} else if err != nil || again[0].Source != mounts[0].Source {
				t.Fatalf("lost stable volume: %v", err)
			}
			if creates != 1 {
				t.Fatalf("recreated retained volume %d times", creates)
			}
		})
	}
}

func TestUnsupportedVolumeSpecsFailBeforeOpeningTarget(t *testing.T) {
	for _, vol := range []cpprovider.VolumeSpec{
		{Name: "data", Type: "bind", MountPath: "/data"},
		{Name: "data", Type: "persistent", MountPath: "/"},
		{Name: "data", Type: "persistent", MountPath: "/data/../etc"},
		{Name: "data", Type: "persistent", MountPath: "/data", SizeMB: 1024},
	} {
		runtime := Runtime{Session: func(context.Context, map[string]string) (*Client, error) {
			t.Fatal("opened target for unsupported storage")
			return nil, nil
		}}
		_, err := runtime.Apply(t.Context(), id.New(id.PrefixInstance), cpprovider.ServiceSpec{Volumes: []cpprovider.VolumeSpec{vol}}, nil)
		if err == nil || !strings.Contains(err.Error(), "volume") {
			t.Fatalf("invalid volume accepted: %+v %v", vol, err)
		}
	}
}

func TestPersistentMountCannotMoveDuringRollout(t *testing.T) {
	iid := id.New(id.PrefixInstance)
	old := &containerInfo{}
	old.Mounts = append(old.Mounts, struct{ Type, Name, Destination string }{"volume", VolumeName(iid, "data"), "/data"})
	for _, volumes := range [][]cpprovider.VolumeSpec{nil, {{Name: "data", Type: "persistent", MountPath: "/elsewhere"}}, {{Name: "other", Type: "persistent", MountPath: "/data"}}} {
		if err := checkVolumeLayout(old, iid, volumes); err == nil {
			t.Fatalf("accepted changed storage: %+v", volumes)
		}
	}
	if err := checkVolumeLayout(old, iid, []cpprovider.VolumeSpec{{Name: "data", Type: "persistent", MountPath: "/data"}}); err != nil {
		t.Fatal(err)
	}
}
