package node

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/xraph/loomplane/internal/clouds/server/fencerules"
	"github.com/xraph/loomplane/pkg/nodeproto"
)

// maxRepairBody bounds the repair request the fence reads.
const maxRepairBody = 64 << 10

// repairReport is the SSH helper's answer to repair-check and repair, which
// server.FenceRepair reads.
type repairReport struct {
	Daemon   string          `json:"daemon"`
	Dirty    json.RawMessage `json:"dirty"`
	Review   string          `json:"review"`
	Running  []runningExec   `json:"running"`
	Repaired bool            `json:"repaired"`
}

type runningExec struct {
	Container string `json:"container"`
	Name      string `json:"name"`
	Exec      string `json:"exec"`
}

type repairRefusal struct {
	Reason string `json:"reason"`
}

// repair is the SSH helper's repair-check and repair modes, an operator's
// reviewed recovery of a dirty journal. The daemon's identity is already
// checked. It never waits for the lock: holding it without waiting shows no
// fenced request is in flight. It reports the journal's mark, a digest of
// the journal and every exec still running in a Loomplane container. In
// repair mode it then clears the mark, keeping the epochs, but only when
// the request names that digest and no such exec runs.
func (d *dockerFence) repair(ctx context.Context, f nodeproto.Fence, j *FenceJournal, body *os.File, out *relay) error {
	locked, err := j.TryLock()
	if err != nil {
		return err
	}
	if !locked {
		out.reply(http.StatusConflict, repairRefusal{"a Loomplane request holds the fence lock, so a Docker change may still be in flight; try again once it finishes"})
		return nil
	}
	defer j.Unlock()
	state, raw, err := j.read()
	if err != nil {
		return err
	}
	dirty := json.RawMessage("false")
	if len(raw) > 0 {
		var fields struct {
			Dirty json.RawMessage `json:"dirty"`
		}
		err = json.Unmarshal(raw, &fields)
		if err != nil {
			return err
		}
		if len(fields.Dirty) > 0 {
			dirty = fields.Dirty
		}
	}
	running, err := d.runningExecs(ctx)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	report := repairReport{Daemon: f.Daemon, Dirty: dirty, Review: hex.EncodeToString(sum[:]), Running: running}
	if f.Mode == nodeproto.FenceRepair {
		var want struct {
			Review string `json:"review"`
		}
		if err = json.NewDecoder(io.LimitReader(body, maxRepairBody)).Decode(&want); err != nil {
			return fmt.Errorf("reading the repair request: %w", err)
		}
		if want.Review != report.Review {
			out.reply(http.StatusConflict, repairRefusal{"the fence journal changed since it was reviewed; review again"})
			return nil
		}
		if len(running) > 0 {
			out.reply(http.StatusConflict, repairRefusal{"an exec is still running in a Loomplane container; wait for it to finish, then review again"})
			return nil
		}
		if state.Dirty.Set {
			// The epochs stay, so an older attempt still can't write.
			state.Dirty = fencerules.Dirty{}
			err = j.Save(state)
			if err != nil {
				return err
			}
			report.Repaired = true
		}
	}
	out.reply(http.StatusOK, report)
	return nil
}

// runningExecs lists the execs still running in containers labelled
// loomplane.org. An exec there may be the change that left the mark. A
// container or exec Docker no longer knows is skipped.
func (d *dockerFence) runningExecs(ctx context.Context) ([]runningExec, error) {
	filters, err := json.Marshal(map[string][]string{"label": {"loomplane.org"}})
	if err != nil {
		return nil, err
	}
	var listed []struct {
		ID string `json:"Id"`
	}
	err = d.getJSON(ctx, "/containers/json?filters="+url.QueryEscape(string(filters)), &listed)
	if err != nil {
		return nil, err
	}
	running := []runningExec{}
	for _, summary := range listed {
		if !isDockerID(summary.ID) {
			return nil, errors.New("the daemon listed a container without a full ID")
		}
		var c struct {
			Name    string
			ExecIDs []string
		}
		err = d.getJSON(ctx, "/containers/"+summary.ID+"/json", &c)
		if errors.Is(err, errDockerNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, id := range c.ExecIDs {
			if !isDockerID(id) {
				return nil, errors.New("the daemon listed an exec without a full ID")
			}
			var e execJSON
			err = d.getJSON(ctx, "/exec/"+id+"/json", &e)
			if errors.Is(err, errDockerNotFound) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if e.Running == nil || *e.Running {
				running = append(running, runningExec{Container: summary.ID, Name: strings.TrimPrefix(c.Name, "/"), Exec: id})
			}
		}
	}
	return running, nil
}

// isDockerID reports whether id is a full Docker ID, 64 lowercase hex.
func isDockerID(id string) bool {
	if len(id) != 64 {
		return false
	}
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
