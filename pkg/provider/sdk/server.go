package sdk

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// MaxSource is the largest source a provider accepts.
const MaxSource = 512 << 20

var sourceID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// server serves a Provider over the protocol.
type server struct {
	p     Provider
	token string
	dir   string

	mu            sync.Mutex
	ops           map[string]*Operation
	logs          map[string]*operationOutput
	logGeneration uint64
}

// Handler serves p over the protocol, refusing any request without token.
// Sources go to a directory under os.TempDir; operations are kept in
// memory, so a restarted provider has none, and Loomplane starts them
// again.
func Handler(p Provider, token string) http.Handler {
	s := &server{p: p, token: token, ops: map[string]*Operation{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/describe", s.describe)
	mux.HandleFunc("POST /v1/check", s.check)
	mux.HandleFunc("HEAD /v1/sources/{id}", s.hasSource)
	mux.HandleFunc("PUT /v1/sources/{id}", s.putSource)
	mux.HandleFunc("POST /v1/operations", s.start)
	mux.HandleFunc("GET /v1/operations/{id}", s.operation)
	mux.HandleFunc("GET /v1/operations/{id}/logs", s.operationLogs)
	mux.HandleFunc("POST /v1/observe", s.observe)
	return s.auth(mux)
}

func (s *server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if s.token == "" || !ok || subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
			reply(w, http.StatusUnauthorized, Failure{Error: "this provider needs its token"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v) //nolint:errcheck // the client went away
}

func fail(w http.ResponseWriter, status int, err error) {
	reply(w, status, Failure{Error: err.Error(), Rejected: errors.Is(err, ErrRejected)})
}

func (s *server) describe(w http.ResponseWriter, _ *http.Request) {
	d := Description{Protocol: Protocol, Provider: s.p.Describe()}
	if logger, ok := s.p.(OperationLogger); ok {
		d.OperationLogs = logger.OperationLogs()
	}
	if c, ok := s.p.(Connector); ok {
		d.Connection = c.ConnectionForm()
	} else {
		d.Connection = ConnectionForm{Title: d.Provider.Title, Credential: "Credential"}
	}
	reply(w, http.StatusOK, d)
}

func (s *server) check(w http.ResponseWriter, r *http.Request) {
	var c Credential
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&c); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	a, err := s.p.Check(r.Context(), c)
	switch {
	case errors.Is(err, ErrRejected):
		fail(w, http.StatusUnprocessableEntity, err)
	case err != nil:
		fail(w, http.StatusBadGateway, err)
	default:
		reply(w, http.StatusOK, a)
	}
}

func (s *server) sourcePath(id string) (string, error) {
	if !sourceID.MatchString(id) {
		return "", fmt.Errorf("%q isn't a source ID", id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dir == "" {
		dir, err := os.MkdirTemp("", "loomplane-provider-sources-")
		if err != nil {
			return "", err
		}
		s.dir = dir
	}
	return filepath.Join(s.dir, id+".tar.gz"), nil
}

func (s *server) hasSource(w http.ResponseWriter, r *http.Request) {
	p, err := s.sourcePath(r.PathValue("id"))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if _, err := os.Stat(p); err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *server) putSource(w http.ResponseWriter, r *http.Request) {
	p, err := s.sourcePath(r.PathValue("id"))
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), "upload-*")
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	defer os.Remove(tmp.Name())
	n, err := io.Copy(tmp, io.LimitReader(r.Body, MaxSource+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	switch {
	case err != nil:
		fail(w, http.StatusBadRequest, err)
		return
	case n > MaxSource:
		fail(w, http.StatusRequestEntityTooLarge, fmt.Errorf("the source is over %d bytes", MaxSource))
		return
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// operationID is the same for every attempt at one job, so a retry finds
// an operation still running instead of starting a second.
func operationID(kind, key string) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + key))
	return kind + "-" + hex.EncodeToString(sum[:12])
}

func (s *server) start(w http.ResponseWriter, r *http.Request) {
	var req OperationRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<20)).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if req.Kind != OpApply && req.Kind != OpRemove || req.Job.Key == "" {
		fail(w, http.StatusBadRequest, errors.New("an operation is an apply or a remove, with a job key"))
		return
	}
	job := req.Job
	if req.Source != "" {
		p, err := s.sourcePath(req.Source)
		if err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		if _, err := os.Stat(p); err != nil {
			fail(w, http.StatusPreconditionFailed, fmt.Errorf("source %s hasn't been uploaded", req.Source))
			return
		}
		job.source = func() (io.ReadCloser, error) { return os.Open(p) }
	}
	id := operationID(req.Kind, job.Key)
	s.mu.Lock()
	if op, ok := s.ops[id]; ok && op.Status == StatusRunning {
		cp := *op
		s.mu.Unlock()
		reply(w, http.StatusAccepted, cp)
		return
	}
	op := &Operation{ID: id, Kind: req.Kind, Status: StatusRunning}
	s.ops[id] = op
	s.enableOutput(id)
	epoch := ""
	if output := s.logs[id]; output != nil {
		epoch = output.epoch
		op.ExecutionID = epoch
	}
	started := *op
	s.mu.Unlock()

	job.log = func(stream, message string) { s.appendOutput(id, epoch, stream, message) }
	job.progress = func(m string) { s.update(id, func(o *Operation) { o.Progress = m }) }
	job.checkpoint = func(b []byte) {
		cp := append([]byte(nil), b...)
		s.update(id, func(o *Operation) { o.Checkpoint = cp })
	}
	go s.run(id, req.Kind, &job)
	reply(w, http.StatusAccepted, started)
}

// run carries out an operation on its own context: it outlives the request
// that started it.
func (s *server) run(id, kind string, job *Job) {
	ctx := context.Background()
	var out *Outcome
	var err error
	func() {
		defer func() {
			if v := recover(); v != nil {
				err = fmt.Errorf("the provider panicked: %v", v)
			}
		}()
		if kind == OpApply {
			out, err = s.p.Apply(ctx, job)
		} else {
			err = s.p.Remove(ctx, job)
		}
	}()
	s.update(id, func(o *Operation) {
		if err != nil {
			o.Status, o.Error = StatusFailed, err.Error()
			return
		}
		o.Status, o.Outcome = StatusSucceeded, out
	})
}

func (s *server) update(id string, f func(*Operation)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if op, ok := s.ops[id]; ok {
		f(op)
	}
}

func (s *server) operation(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	op, ok := s.ops[r.PathValue("id")]
	var cp Operation
	if ok {
		cp = *op
	}
	s.mu.Unlock()
	if !ok {
		fail(w, http.StatusNotFound, errors.New("no such operation; start it again"))
		return
	}
	reply(w, http.StatusOK, cp)
}

func (s *server) observe(w http.ResponseWriter, r *http.Request) {
	var t Target
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&t); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	o, err := s.p.Observe(r.Context(), &t)
	if err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	reply(w, http.StatusOK, o)
}
