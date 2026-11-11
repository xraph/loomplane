package cloudinfra

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
)

// namespaceCommitProxy drops one acknowledged COMMIT response after PostgreSQL
// reaches ReadyForQuery. It never changes the SQL or creates a fake transaction.
type namespaceCommitProxy struct {
	listener      net.Listener
	backend       string
	armed         atomic.Bool
	lost          chan struct{}
	mu            sync.Mutex
	connections   map[net.Conn]bool
	closing       bool
	stopped       chan struct{}
	reconcileGate *namespaceReconcileGate
	workers       sync.WaitGroup
}

func newNamespaceCommitProxy(t *testing.T, dsn string) (*namespaceCommitProxy, string) {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if u.Query().Get("sslmode") != "disable" {
		t.Fatal("commit-loss fixture requires local sslmode=disable")
	}
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &namespaceCommitProxy{listener: listener, backend: u.Host, lost: make(chan struct{}), connections: make(map[net.Conn]bool), stopped: make(chan struct{})}
	p.workers.Add(1)
	go func() {
		defer p.workers.Done()
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			p.mu.Lock()
			if p.closing {
				p.mu.Unlock()
				_ = client.Close()
				return
			}
			p.connections[client] = true
			p.workers.Add(1)
			p.mu.Unlock()
			go p.forward(client)
		}
	}()
	t.Cleanup(func() {
		p.mu.Lock()
		p.closing = true
		close(p.stopped)
		_ = p.listener.Close()
		for conn := range p.connections {
			_ = conn.Close()
		}
		p.mu.Unlock()
		p.workers.Wait()
	})
	u.Host = listener.Addr().String()
	return p, u.String()
}

func (p *namespaceCommitProxy) forward(client net.Conn) {
	defer p.workers.Done()
	defer func() { _ = client.Close(); p.mu.Lock(); delete(p.connections, client); p.mu.Unlock() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	backend, err := (&net.Dialer{}).DialContext(ctx, "tcp", p.backend)
	cancel()
	if err != nil {
		return
	}
	defer func() { _ = backend.Close(); p.mu.Lock(); delete(p.connections, backend); p.mu.Unlock() }()
	p.mu.Lock()
	if p.closing {
		p.mu.Unlock()
		return
	}
	p.connections[backend] = true
	p.mu.Unlock()
	// Local fixtures disable SSL, so the first frame is a normal StartupMessage.
	var header [4]byte
	if _, err = io.ReadFull(client, header[:]); err != nil {
		return
	}
	size := int(binary.BigEndian.Uint32(header[:]))
	if size < 8 || size > 1024*1024 {
		return
	}
	startup := make([]byte, size)
	copy(startup, header[:])
	if _, err = io.ReadFull(client, startup[4:]); err != nil {
		return
	}
	if _, err = backend.Write(startup); err != nil {
		return
	}
	var drop atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() { _ = client.Close(); _ = backend.Close() }()
		for {
			frame, err := namespaceReadFrame(backend)
			if err != nil {
				return
			}
			if drop.Load() {
				if frame[0] == 'Z' {
					close(p.lost)
					return
				}
				continue
			}
			if _, err = client.Write(frame); err != nil {
				return
			}
		}
	}()
forwardFrames:
	for {
		frame, err := namespaceReadFrame(client)
		if err != nil {
			break
		}
		p.mu.Lock()
		gate := p.reconcileGate
		p.mu.Unlock()
		if gate != nil && namespaceReconcileQuery(frame) {
			gate.startedOnce.Do(func() { close(gate.started) })
			select {
			case <-gate.release:
			case <-p.stopped:
				break forwardFrames
			}
		}
		if frame[0] == 'Q' && strings.EqualFold(strings.TrimSpace(strings.TrimRight(string(frame[5:]), "\x00")), "commit") && p.armed.CompareAndSwap(true, false) {
			drop.Store(true)
		}
		if _, err = backend.Write(frame); err != nil {
			break
		}
	}
	_ = client.Close()
	_ = backend.Close()
	<-done
}

func namespaceReadFrame(reader io.Reader) ([]byte, error) {
	var header [5]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return nil, err
	}
	size := int(binary.BigEndian.Uint32(header[1:]))
	if size < 4 || size > 4*1024*1024 {
		return nil, io.ErrUnexpectedEOF
	}
	frame := make([]byte, size+1)
	copy(frame, header[:])
	_, err := io.ReadFull(reader, frame[5:])
	return frame, err
}

func TestNamespacePostgresLostCommitResponseReconcilesSameAttempt(t *testing.T) {
	f := newNamespacePGFixture(t, "namespace-commit")
	f.proxy.armed.Store(true)
	result, err := f.transactions.run(t.Context(), f.action)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-f.proxy.lost:
	default:
		t.Fatal("fixture did not lose a real COMMIT response")
	}
	if result == nil || result.Record.CurrentAttemptID != f.action.AttemptID {
		t.Fatal("unknown commit released the wrong result")
	}
	var count int
	if err = f.db.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_fleet_namespace_attempts`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("commit reconciliation duplicated effect: %d %v", count, err)
	}
}

func TestNamespacePostgresLostReceiptCommitResponseReconcilesSameAttempt(t *testing.T) {
	f := newNamespacePGFixture(t, "receipt-commit")
	first, err := f.transactions.run(t.Context(), f.action)
	if err != nil {
		t.Fatal(err)
	}
	o := namespaceCloneOperation(t, first.Operation)
	at := time.Now().UTC()
	o.Enrollment = &ClusterEnrollment{AttachmentID: "attachment", RuntimeConnectionID: "runtime-connection", TargetID: "runtime-target", ResourceRevision: 1, State: kubernetes.EnrollmentState{Attempted: map[string]bool{"Namespace": true}, Objects: map[string]string{}}}
	o.Steps[1].State = "attempted"
	o.Steps[1].AttemptedAt = &at
	o.Revision++
	if err = f.transactions.Checkpoint(t.Context(), first.Record, &o, 1); err != nil {
		t.Fatal(err)
	}
	o.Enrollment.State.NamespaceUID = "namespace-uid"
	f.proxy.armed.Store(true)
	if err = f.transactions.preserveOutcome(t.Context(), &o); err != nil {
		t.Fatal(err)
	}
	select {
	case <-f.proxy.lost:
	default:
		t.Fatal("fixture did not lose a real receipt COMMIT response")
	}
	saved, err := f.transactions.store.getNamespaceAttemptOutcome(t.Context(), o.OrgID, o.ID, o.NamespaceAttemptID)
	if err != nil || saved.AttemptID != o.NamespaceAttemptID || saved.InputDigest != o.NamespaceInputDigest || saved.State.NamespaceUID != "namespace-uid" {
		t.Fatalf("wrong recovered receipt: %+v %v", saved, err)
	}
}

type namespaceReconcileGate struct {
	started, release         chan struct{}
	startedOnce, releaseOnce sync.Once
}

func (g *namespaceReconcileGate) unblock() { g.releaseOnce.Do(func() { close(g.release) }) }
func namespaceReconcileQuery(frame []byte) bool {
	if len(frame) < 6 {
		return false
	}
	payload := string(frame[5:])
	if frame[0] == 'P' {
		_, after, ok := strings.Cut(payload, "\x00")
		if !ok {
			return false
		}
		payload = after
	} else if frame[0] != 'Q' {
		return false
	}
	query, _, _ := strings.Cut(payload, "\x00")
	return strings.HasPrefix(query, "SELECT input,result,authorized_at,authorized_at_ns,NOT pg_catalog.pg_is_in_recovery() FROM ") && strings.Contains(query, `"loomplane_fleet_namespace_attempts"`)
}
