// Package fencerules holds the checks a daemon fence applies before it lets a
// Docker request through. The SSH helper enforces them in Python (see
// effectFenceScript in the parent package); this is the Go mirror the node
// uses. It imports nothing from internal/, so the node can use it without
// pulling in the server package.
package fencerules

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
)

// Journal is the daemon-wide fence state. Keys of Epochs are JournalKey of
// the operation, exactly as the Python helper writes them.
type Journal struct {
	Epochs map[string]int64 `json:"epochs"`
	Dirty  bool             `json:"dirty"`
}

// ErrDirty means a request died mid-flight, so nothing more may run until an
// operator clears the journal.
var ErrDirty = errors.New("fence journal is dirty")

// JournalKey is the Epochs key for an operation: sha256(operation) in hex.
func JournalKey(operation string) string {
	sum := sha256.Sum256([]byte(operation))
	return hex.EncodeToString(sum[:])
}

// Advance raises the operation's epoch to generation. It refuses a
// generation below the current one.
func Advance(j *Journal, operation string, generation int64) error {
	if j.Dirty {
		return ErrDirty
	}
	key := JournalKey(operation)
	if generation < j.Epochs[key] {
		return fmt.Errorf("generation %d is older than the current %d", generation, j.Epochs[key])
	}
	if j.Epochs == nil {
		j.Epochs = map[string]int64{}
	}
	j.Epochs[key] = generation
	return nil
}

// Admit requires generation to be the operation's current, non-zero epoch.
func Admit(j Journal, operation string, generation int64) error {
	if j.Dirty {
		return ErrDirty
	}
	current := j.Epochs[JournalKey(operation)]
	if generation != current || current <= 0 {
		return fmt.Errorf("generation %d is not the current %d", generation, current)
	}
	return nil
}

// ExecInspect is the part of GET /exec/{id}/json the fence reads.
type ExecInspect struct {
	ContainerID string
	Running     bool
	Entrypoint  string
	Args        []string
	TTY         bool
	Privileged  bool
}

// ContainerInspect is the part of GET /containers/{id}/json the fence reads.
type ContainerInspect struct {
	ID      string
	Image   string
	Running bool
	Labels  map[string]string
}

var (
	execStartPath      = regexp.MustCompile(`^/exec/[a-f0-9]{64}/start$`)
	containerIDPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
	probeHost          = regexp.MustCompile(`^[a-z0-9.-]+$`)
	zoneID             = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)
)

// CheckExec covers the checks shared by the exec and ingress-exec modes: the
// request starts an attached, non-TTY exec that is not yet running and not
// privileged.
func CheckExec(mode, method, path string, body []byte, exec ExecInspect) error {
	if mode != "exec" && mode != "ingress-exec" {
		return fmt.Errorf("mode %q is not an exec mode", mode)
	}
	if method != "POST" || !execStartPath.MatchString(path) {
		return fmt.Errorf("%s %s is not an exec start", method, path)
	}
	var want map[string]any
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&want); err != nil || dec.More() {
		return errors.New("exec start body is not a single JSON object")
	}
	if !reflect.DeepEqual(want, map[string]any{"Detach": false, "Tty": false}) {
		return errors.New(`exec start body must be {"Detach":false,"Tty":false}`)
	}
	if !containerIDPattern.MatchString(exec.ContainerID) {
		return errors.New("exec has no container")
	}
	if exec.Running {
		return errors.New("exec is already running")
	}
	if exec.TTY || exec.Privileged {
		return errors.New("exec must not use a TTY or privileges")
	}
	return nil
}

// CheckIngressExec pins which command an ingress-exec may run, in which
// container, for which operation.
func CheckIngressExec(exec ExecInspect, owned ContainerInspect, operation string) error {
	args := exec.Args
	if exec.Entrypoint != "sh" || len(args) == 0 || args[0] != "-c" {
		return errors.New("ingress exec must run sh -c")
	}
	admin := len(args) == 6 && args[1] == IngressAdminScript && args[2] == "ingress" &&
		slices.Contains([]string{"read", "apply", "reload"}, args[3])
	probe := len(args) == 4 && args[1] == IngressProbeScript && args[2] == "probe" && probeHost.MatchString(args[3])
	token := len(args) == 4 && args[1] == IngressTokenScript && args[2] == "dns" && zoneID.MatchString(args[3])
	if !admin && !probe && !token {
		return errors.New("ingress exec is not one of the allowed commands")
	}
	if owned.ID != exec.ContainerID || !owned.Running {
		return errors.New("ingress container is not the exec's running container")
	}
	if !slices.Contains(IngressImages(), owned.Image) {
		return fmt.Errorf("image %q is not an ingress image", owned.Image)
	}
	org, ingress := owned.Labels["loomplane.org"], owned.Labels["loomplane.ingress"]
	if org == "" || ingress == "" {
		return errors.New("container is not an owned ingress proxy")
	}
	if operation != org+"/ingress/"+ingress {
		return fmt.Errorf("operation %q does not own this ingress proxy", operation)
	}
	return nil
}
