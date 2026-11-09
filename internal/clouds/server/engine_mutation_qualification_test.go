//go:build qualification

package server

import (
	"context"
	"errors"
	"maps"
	"net"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/xraph/ctrlplane/id"

	"github.com/xraph/loomplane/internal/clouds/owned"
)

func TestQualifyInstanceClassDockerCollision(t *testing.T) {
	socket := os.Getenv("LOOMPLANE_TEST_DOCKER_SOCKET")
	if socket == "" {
		t.Skip("explicit local Docker socket required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	client := &Client{Config: &Config{}, HTTP: &http.Client{Transport: transport}}
	iid := id.New(id.PrefixInstance)
	name := instanceClassName(iid)
	labels := map[string]string{owned.LabelOrg: "task5b", owned.LabelProject: "task5b", owned.LabelEnvironment: iid.String(), owned.LabelConnection: "local", owned.LabelService: "collision"}
	ordinary, err := classLabels(iid, labels, "ordinary")
	if err != nil {
		t.Fatal(err)
	}
	duck, err := classLabels(iid, labels, "duckdb")
	if err != nil {
		t.Fatal(err)
	}
	var absent instanceClassVolume
	if err := dockerJSON(ctx, client, http.MethodGet, "/volumes/"+name, nil, &absent); !errors.Is(err, ErrMissing) {
		t.Fatalf("task volume already exists: %v", err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		var stored instanceClassVolume
		if err := dockerJSON(cleanup, client, http.MethodGet, "/volumes/"+name, nil, &stored); errors.Is(err, ErrMissing) {
			return
		} else if err != nil {
			t.Error(err)
			return
		}
		if stored.Name != name || (!maps.Equal(stored.Labels, ordinary) && !maps.Equal(stored.Labels, duck)) {
			t.Error("task class volume ownership changed; retained")
			return
		}
		if err := dockerJSON(cleanup, client, http.MethodDelete, "/volumes/"+name, nil, nil); err != nil {
			t.Error(err)
		}
	})
	var results [2]instanceClassVolume
	var errs [2]error
	start := make(chan struct{})
	var workers sync.WaitGroup
	for index, proposal := range []map[string]string{ordinary, duck} {
		workers.Go(func() {
			<-start
			errs[index] = dockerJSON(ctx, client, http.MethodPost, "/volumes/create", map[string]any{"Name": name, "Driver": "local", "Labels": proposal}, &results[index])
		})
	}
	close(start)
	workers.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if !maps.Equal(results[0].Labels, results[1].Labels) {
		t.Fatal("Docker returned inconsistent collision labels")
	}
	winner := results[0].Labels
	if !maps.Equal(winner, ordinary) && !maps.Equal(winner, duck) {
		t.Fatal("Docker stored an unknown proposal")
	}
	for index := range results {
		if err := validateClassVolume(&results[index], name, winner); err != nil {
			t.Fatal(err)
		}
	}
	var stored instanceClassVolume
	if err := dockerJSON(ctx, client, http.MethodGet, "/volumes/"+name, nil, &stored); err != nil {
		t.Fatal(err)
	}
	if err := validateClassVolume(&stored, name, winner); err != nil {
		t.Fatal(err)
	}
	loser := ordinary
	if maps.Equal(winner, ordinary) {
		loser = duck
	}
	if err := validateClassVolume(&stored, name, loser); err == nil {
		t.Fatal("losing class accepted stored volume")
	}
	t.Log("concurrent Docker create/get retained one immutable class and stored labels")
}
