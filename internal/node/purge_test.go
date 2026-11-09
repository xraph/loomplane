package node

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/xraph/loomplane/pkg/nodeproto"
)

// purgeDaemon is a Docker daemon holding containers, networks and volumes.
// It answers every list with everything it holds, whatever the filter
// asked, so Purge's own label check is what keeps unlabelled objects.
type purgeDaemon struct {
	mu         sync.Mutex
	containers []map[string]any
	networks   []map[string]any
	volumes    []map[string]any
	// deleted is each DELETE in the order it came.
	deleted []string
	// filters are the filters each list asked for.
	filters []string
}

func labelled() map[string]string {
	return map[string]string{loomplaneLabel: "org_1"}
}

func newPurgeDaemon() *purgeDaemon {
	return &purgeDaemon{
		containers: []map[string]any{
			{"Id": "c1aaaaaaaaaaaa", "Names": []string{"/web"}, "Labels": labelled()},
			{"Id": "c2bbbbbbbbbbbb", "Names": []string{"/mine"}, "Labels": map[string]string{"com.example": "x"}},
		},
		networks: []map[string]any{
			{"Id": "n1", "Name": "loomplane-env", "Labels": labelled()},
			{"Id": "n2", "Name": "bridge", "Labels": map[string]string{}},
		},
		volumes: []map[string]any{
			{"Name": "pgdata", "Labels": labelled()},
			{"Name": "photos", "Labels": nil},
		},
	}
}

func (d *purgeDaemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if r.Method == http.MethodDelete {
		d.deleted = append(d.deleted, r.URL.Path+"?"+r.URL.RawQuery)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	d.filters = append(d.filters, r.URL.Query().Get("filters"))
	switch r.URL.Path {
	case "/containers/json":
		_ = json.NewEncoder(w).Encode(d.containers)
	case "/networks":
		_ = json.NewEncoder(w).Encode(d.networks)
	case "/volumes":
		_ = json.NewEncoder(w).Encode(map[string]any{"Volumes": d.volumes})
	default:
		http.NotFound(w, r)
	}
}

func (d *purgeDaemon) deletes() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.deleted)
}

func TestPurgeKeepsVolumesByDefault(t *testing.T) {
	d := newPurgeDaemon()
	sock := fakeDockerAPI(t, d)
	rep, err := Purge(t.Context(), sock, false)
	if err != nil {
		t.Fatal(err)
	}
	// Containers go first, forced, and keep their anonymous volumes;
	// networks after; no volume at all.
	want := []string{"/containers/c1aaaaaaaaaaaa?force=1", "/networks/n1?"}
	if got := d.deletes(); !slices.Equal(got, want) {
		t.Fatalf("deleted %q, want %q", got, want)
	}
	if !slices.Equal(rep.Containers, []string{"web"}) || !slices.Equal(rep.Networks, []string{"loomplane-env"}) ||
		len(rep.Volumes) != 0 || !slices.Equal(rep.KeptVolumes, []string{"pgdata"}) {
		t.Fatalf("report %+v", rep)
	}
	for _, f := range d.filters {
		var got map[string][]string
		if err := json.Unmarshal([]byte(f), &got); err != nil || !slices.Equal(got["label"], []string{loomplaneLabel}) {
			t.Fatalf("a list asked for filters %q", f)
		}
	}
}

func TestPurgeIgnoresUnlabelled(t *testing.T) {
	d := newPurgeDaemon()
	sock := fakeDockerAPI(t, d)
	rep, err := Purge(t.Context(), sock, true)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/containers/c1aaaaaaaaaaaa?force=1", "/networks/n1?", "/volumes/pgdata?"}
	if got := d.deletes(); !slices.Equal(got, want) {
		t.Fatalf("deleted %q, want %q", got, want)
	}
	if !slices.Equal(rep.Volumes, []string{"pgdata"}) || len(rep.KeptVolumes) != 0 {
		t.Fatalf("report %+v", rep)
	}
}

// A daemon that is not there is an error, not an empty purge.
func TestPurgeFailsWithoutADaemon(t *testing.T) {
	if _, err := Purge(t.Context(), filepath.Join(shortDir(t), "none.sock"), false); err == nil {
		t.Fatal("purged a daemon that isn't there")
	}
}

// One object the daemon won't remove doesn't stop the rest, and the error
// names it.
func TestPurgeCarriesOnPastAFailure(t *testing.T) {
	d := newPurgeDaemon()
	d.containers = append(d.containers, map[string]any{"Id": "c3", "Names": []string{"/stuck"}, "Labels": labelled()})
	sock := fakeDockerAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/containers/c3") {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"message":"removal of container c3 is already in progress"}`))
			return
		}
		d.ServeHTTP(w, r)
	}))
	rep, err := Purge(t.Context(), sock, false)
	if err == nil || !strings.Contains(err.Error(), "stuck") || !strings.Contains(err.Error(), "already in progress") {
		t.Fatalf("Purge: %v", err)
	}
	if !slices.Equal(rep.Containers, []string{"web"}) || !slices.Equal(rep.Networks, []string{"loomplane-env"}) {
		t.Fatalf("report %+v", rep)
	}
}

// leaveRig is a joined node, not running, whose Docker is d.
func leaveRig(t *testing.T, d *purgeDaemon, name string) *cmdRig {
	t.Helper()
	r := newCmdRig(t)
	st := State{Dir: r.dir}
	if _, err := st.Identity(); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveConfig(Config{URL: "http://x", NodeID: "node_1", Name: name, Endpoints: []nodeproto.Endpoint{nodeproto.EndpointDocker},
		DockerSocket: fakeDockerAPI(t, d)}); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestPurgeVolumesNeedsName(t *testing.T) {
	refused := func(t *testing.T, r *cmdRig, d *purgeDaemon, err error, says string) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), says) {
			t.Fatalf("leave: %v\n%s", err, r.out)
		}
		// Nothing changed: no Docker object, no service, no config.
		if got := d.deletes(); len(got) != 0 {
			t.Fatalf("deleted %q", got)
		}
		if len(r.svc.calls) != 0 {
			t.Fatalf("the service was touched: %v", r.svc.calls)
		}
		if _, lerr := (State{Dir: r.dir}).LoadConfig(); lerr != nil {
			t.Fatalf("node.json must stay: %v", lerr)
		}
	}

	t.Run("wrong name typed", func(t *testing.T) {
		d := newPurgeDaemon()
		r := leaveRig(t, d, "web-1")
		r.opts.isTerminal = func() bool { return true }
		r.opts.in = strings.NewReader("web-2\n")
		err := r.run("node", "leave", "--purge-volumes", "--state-dir", r.dir)
		if !strings.Contains(r.out.String(), "Type the node's name to delete its Loomplane volumes: ") {
			t.Fatalf("no prompt:\n%s", r.out)
		}
		refused(t, r, d, err, "isn't this node's name")
	})
	t.Run("no terminal", func(t *testing.T) {
		d := newPurgeDaemon()
		r := leaveRig(t, d, "web-1")
		refused(t, r, d, r.run("node", "leave", "--purge-volumes", "--state-dir", r.dir), "--yes-delete-volumes")
	})
	t.Run("wrong flag", func(t *testing.T) {
		d := newPurgeDaemon()
		r := leaveRig(t, d, "web-1")
		refused(t, r, d, r.run("node", "leave", "--purge-volumes", "--yes-delete-volumes=web-2", "--state-dir", r.dir), "isn't this node's name")
	})
	t.Run("name unknown", func(t *testing.T) {
		d := newPurgeDaemon()
		r := leaveRig(t, d, "")
		refused(t, r, d, r.run("node", "leave", "--purge-volumes", "--yes-delete-volumes=web-1", "--state-dir", r.dir), "doesn't know its node's name")
	})
	t.Run("right name typed", func(t *testing.T) {
		d := newPurgeDaemon()
		r := leaveRig(t, d, "web-1")
		r.opts.isTerminal = func() bool { return true }
		r.opts.in = strings.NewReader(" web-1 \n")
		if err := r.run("node", "leave", "--purge-volumes", "--state-dir", r.dir); err != nil {
			t.Fatalf("leave: %v\n%s", err, r.out)
		}
		if got := d.deletes(); len(got) != 3 || got[2] != "/volumes/pgdata?" {
			t.Fatalf("deleted %q", got)
		}
		if !strings.Contains(r.out.String(), "pgdata") {
			t.Fatalf("the report doesn't name the volume:\n%s", r.out)
		}
	})
	t.Run("flag skips the prompt", func(t *testing.T) {
		d := newPurgeDaemon()
		r := leaveRig(t, d, "web-1")
		if err := r.run("node", "leave", "--purge-volumes", "--yes-delete-volumes=web-1", "--state-dir", r.dir); err != nil {
			t.Fatalf("leave: %v\n%s", err, r.out)
		}
		if strings.Contains(r.out.String(), "Type the node's name") {
			t.Fatalf("asked anyway:\n%s", r.out)
		}
		if got := d.deletes(); len(got) != 3 {
			t.Fatalf("deleted %q", got)
		}
	})
}

// --purge alone removes containers and networks after the node has left,
// keeps the volumes and says how to remove them by hand.
func TestLeavePurgeKeepsVolumes(t *testing.T) {
	d := newPurgeDaemon()
	r := leaveRig(t, d, "web-1")
	if err := r.run("node", "leave", "--purge", "--state-dir", r.dir); err != nil {
		t.Fatalf("leave: %v\n%s", err, r.out)
	}
	if got := d.deletes(); len(got) != 2 {
		t.Fatalf("deleted %q", got)
	}
	out := r.out.String()
	for _, want := range []string{"web", "loomplane-env", "pgdata", "docker volume rm pgdata"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}
	if _, err := os.Stat(filepath.Join(r.dir, "node.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("node.json stayed: %v", err)
	}
}

// Leave without --purge touches no Docker object, as in phase 1.
func TestLeaveWithoutPurgeLeavesDockerAlone(t *testing.T) {
	d := newPurgeDaemon()
	r := leaveRig(t, d, "web-1")
	if err := r.run("node", "leave", "--state-dir", r.dir); err != nil {
		t.Fatalf("leave: %v\n%s", err, r.out)
	}
	if got := d.deletes(); len(got) != 0 {
		t.Fatalf("deleted %q", got)
	}
}

// When the running node can't get Loomplane to confirm, leave keeps the
// service and config as before, but --purge still cleans up the machine.
func TestLeavePurgesWhenLoomplaneIsUnreachable(t *testing.T) {
	d := newPurgeDaemon()
	r := leaveRig(t, d, "web-1")
	serveControlStub(t, r.dir, `{"ok":false,"error":"Loomplane did not confirm"}`)
	err := r.run("node", "leave", "--purge", "--state-dir", r.dir)
	if err == nil || !strings.Contains(err.Error(), "kept") {
		t.Fatalf("leave: %v", err)
	}
	if !strings.Contains(r.out.String(), "Revoke it in the dashboard") {
		t.Fatalf("no hint:\n%s", r.out)
	}
	if got := d.deletes(); len(got) != 2 {
		t.Fatalf("deleted %q", got)
	}
	if _, lerr := (State{Dir: r.dir}).LoadConfig(); lerr != nil {
		t.Fatalf("node.json must stay: %v", lerr)
	}
}

// node.json keeps the name Loomplane gave the node, which leave asks for
// before it deletes volumes.
func TestJoinSavesTheNodesName(t *testing.T) {
	r := newRig(t)
	if cfg := r.join(t, nodeproto.EndpointProbe); cfg.Name != "web-1" {
		t.Fatalf("joined as %q", cfg.Name)
	}
	cfg, err := r.st.LoadConfig()
	if err != nil || cfg.Name != "web-1" {
		t.Fatalf("node.json name %q (%v)", cfg.Name, err)
	}
}
