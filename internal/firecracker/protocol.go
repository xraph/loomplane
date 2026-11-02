// Package firecracker implements the standalone host control protocol.
package firecracker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const ProtocolVersion = 1
const MaxControlBytes = 1 << 20
const MaxSourceBytes = 2 << 30
const MaxLogBytes = 256 << 10
const GuestPort = 10789

var ErrInvalid = errors.New("invalid Firecracker request")
var ErrConflict = errors.New("firecracker operation conflicts with recorded ownership or generation")
var ErrMissing = errors.New("firecracker workload not found")
var ErrCapacity = errors.New("firecracker host capacity exhausted")
var ErrUnavailable = errors.New("firecracker isolation is unavailable")
var identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.:-]{0,159}$`)
var digestHex = regexp.MustCompile(`^[a-f0-9]{64}$`)
var serviceLabel = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
var envName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
var pinnedImage = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9./_:-]*@sha256:[a-f0-9]{64}$`)

type Scope struct{ Org, Connection, Project, Environment, Resource string }

func (s Scope) Valid() bool {
	for _, v := range []string{s.Org, s.Connection, s.Project, s.Environment, s.Resource} {
		if !identifier.MatchString(v) {
			return false
		}
	}
	return true
}
func (s Scope) Key() string {
	raw := s.Org + "/" + s.Connection + "/" + s.Resource
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
func (s Scope) Network() string {
	sum := sha256.Sum256([]byte(s.Org + "/" + s.Project + "/" + s.Environment))
	return hex.EncodeToString(sum[:12])
}

type Authority struct {
	Generation int64
	Operation  string
	Expires    time.Time
}
type Spec struct {
	SourceDigest              string
	Service                   string
	Kind                      string
	Image                     string
	VCPUs, MemoryMiB, DiskMiB int
	Port, PublishedPort       int
	HealthPath                string
	Command, Args             []string
	BuildImage, Dockerfile    string
}
type Values struct {
	Env            map[string]string
	RegistryConfig []byte
}
type NetworkValues struct {
	Peers map[string]string
	DNS   []string
}
type Request struct {
	Network         NetworkValues
	Route           *RouteSpec `json:",omitempty"`
	Scope           Scope
	Authority       Authority
	RuntimeIdentity string
	Spec            Spec
	Values          Values
}
type VM struct {
	Created time.Time
	ID      string
	Slot    int
	Spec    Spec
	Network string
}
type Record struct {
	Scope                              Scope
	Generation                         int64
	Operation, Digest, RuntimeIdentity string
	State                              string
	Current, Pending                   *VM
	Garbage                            []VM
	Updated                            time.Time
}
type GuestStatus struct {
	State          string
	Ready          bool
	ExitCode       int
	Digest, Output string
}
type Capabilities struct {
	Version                               int
	HostID, Architecture, RuntimeIdentity string
	IngressAddress                        string
	Isolation                             bool
	Limits                                Limits
}
type Limits struct{ VMs, VCPUs, MemoryMiB, DiskMiB int }

func (q Request) Validate(now time.Time) error {
	if err := q.ValidateAuthority(now); err != nil {
		return err
	}
	s := q.Spec
	if s.Kind != "app" && s.Kind != "build" {
		return ErrInvalid
	}
	if s.VCPUs < 1 || s.VCPUs > 32 || s.MemoryMiB < 128 || s.MemoryMiB > 131072 || s.DiskMiB < 256 || s.DiskMiB > 1048576 || s.Port < 0 || s.Port > 65535 || s.PublishedPort < 0 || s.PublishedPort > 65535 || (s.PublishedPort > 0 && s.PublishedPort < 1024) || (s.PublishedPort > 0 && s.Port == 0) {
		return ErrInvalid
	}
	if s.Kind == "app" && (!pinnedImage.MatchString(s.Image) || !serviceLabel.MatchString(s.Service)) {
		return fmt.Errorf("%w: applications require a digest-pinned OCI image", ErrInvalid)
	}
	if s.Kind == "build" && (!digestHex.MatchString(s.SourceDigest) || s.Dockerfile != ".loomplane-build/Dockerfile" || !buildTag.MatchString(s.BuildImage) || s.PublishedPort != 0) {
		return ErrInvalid
	}
	if s.HealthPath != "" && (!strings.HasPrefix(s.HealthPath, "/") || strings.ContainsAny(s.HealthPath, "\r\n") || len(s.HealthPath) > 2048) {
		return ErrInvalid
	}
	if len(s.Command) > 64 || len(s.Args) > 128 || len(q.Values.Env) > 256 || len(q.Values.RegistryConfig) > 128<<10 {
		return ErrInvalid
	}
	for _, args := range [][]string{s.Command, s.Args} {
		for _, arg := range args {
			if len(arg) > 8192 || strings.ContainsRune(arg, 0) {
				return ErrInvalid
			}
		}
	}
	for k, v := range q.Values.Env {
		if !envName.MatchString(k) || strings.ContainsAny(v, "\x00\r\n") || len(k) > 256 || len(v) > 65536 {
			return ErrInvalid
		}
	}
	return nil
}

var buildTag = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9./_:-]{0,500}:[a-f0-9]{24,64}$`)

func Decode(r io.Reader, out any, limit int64) error {
	d := json.NewDecoder(io.LimitReader(r, limit+1))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return ErrInvalid
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		return ErrInvalid
	}
	return nil
}
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v) //nolint:errcheck // Headers are committed; a disconnected peer cannot receive a second response.
}

// Serving retains the last ready revision while its replacement is prepared.
func (r Record) Serving() bool {
	return r.Current != nil && (r.State == "ready" || r.State == "preparing" || r.State == "starting")
}

// NetworkVMs includes staging revisions so private dependencies can answer
// readiness probes before promotion. Discovery still resolves only Current.
func (r Record) NetworkVMs() []*VM {
	if r.State != "ready" && r.State != "preparing" && r.State != "starting" {
		return nil
	}
	return []*VM{r.Current, r.Pending}
}

func (q Request) ValidateAuthority(now time.Time) error {
	if !q.Scope.Valid() || q.Authority.Generation < 1 || !identifier.MatchString(q.Authority.Operation) || !q.Authority.Expires.After(now) || q.Authority.Expires.After(now.Add(5*time.Minute)) || !digestHex.MatchString(q.RuntimeIdentity) {
		return ErrInvalid
	}
	return nil
}
