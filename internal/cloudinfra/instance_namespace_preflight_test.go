package cloudinfra

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/currentauthority"
	"github.com/xraph/loomplane/internal/fleets"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/fleet"
)

type namespaceBlockedPreflight struct {
	InstanceNamespaceAuthority
	entered, cancelled, release chan struct{}
}

func (b *namespaceBlockedPreflight) wait(ctx context.Context) error {
	close(b.entered)
	<-ctx.Done()
	close(b.cancelled)
	<-b.release
	return ctx.Err()
}
func (b *namespaceBlockedPreflight) VerifyInstanceSnapshot(ctx context.Context, _ fleets.OperationAuthority, _ fleets.InstanceSnapshot) (*fleets.InstanceSnapshot, error) {
	return nil, b.wait(ctx)
}
func (b *namespaceBlockedPreflight) GetInstance(ctx context.Context, _ security.Principal, _ string) (*fleet.Instance, error) {
	return nil, b.wait(ctx)
}

type namespacePreflightClouds struct{ Clouds }
type namespacePreflightConnections struct{ connections.Store }

func TestNamespacePublicPreflightRemainsOwnedUntilReleased(t *testing.T) {
	for _, operation := range []string{"reserve", "renew", "receipt"} {
		t.Run(operation, func(t *testing.T) {
			b := &namespaceBlockedPreflight{entered: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{})}
			a := &InstanceNamespaceTransactions{}
			s := NewService(Deps{ManagedClusters: config.ManagedClustersConfig{Enabled: true}, Clusters: &namespaceStatusStore{}, InstanceNamespaces: a, InstanceAuthority: b, InstanceIsolation: namespaceRetryQualification{}, Clouds: namespacePreflightClouds{}, RuntimeConnections: namespacePreflightConnections{}})
			in := namespaceActionFixture()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			defer close(b.release)
			done := make(chan error, 1)
			go func() {
				var err error
				switch operation {
				case "reserve":
					_, err = s.ReserveInstanceNamespace(ctx, in.Principal, InstanceNamespaceInput{Authority: in.Record.CurrentAuthority, Snapshot: in.Snapshot, ClusterID: in.Record.ClusterID})
				case "renew":
					_, err = s.RenewInstanceNamespaceAuthority(ctx, in.Principal, InstanceNamespaceInput{Authority: in.Record.CurrentAuthority, Snapshot: in.Snapshot, ClusterID: in.Record.ClusterID})
				case "receipt":
					_, err = s.GetInstanceNamespaceReceipt(ctx, in.Principal, InstanceNamespaceReceiptQuery{OperationID: in.Operation.ID, InstanceID: in.Snapshot.InstanceID, Generation: 1, OwnershipEpoch: 1, SnapshotDigest: in.Snapshot.Digest})
				}
				done <- err
			}()
			select {
			case <-b.entered:
			case err := <-done:
				t.Fatalf("preflight not entered: %v", err)
			case <-time.After(time.Second):
				t.Fatal("preflight did not start")
			}
			closing, stop := context.WithCancel(t.Context())
			stop()
			err := a.Shutdown(closing)
			if !errors.Is(err, currentauthority.ErrBusy) || a.Drained() {
				t.Errorf("shutdown lost public preflight ownership: %v", err)
			}
			select {
			case <-b.cancelled:
			case <-time.After(time.Second):
				t.Error("shutdown did not cancel public preflight")
			}
			// Cancel the caller too so this test releases a pre-fix blocked call.
			cancel()
			b.release <- struct{}{}
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Errorf("cancelled preflight: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("public call did not finish")
			}
			if err := a.Shutdown(t.Context()); err != nil || !a.Drained() {
				t.Fatalf("final public drain: %v", err)
			}
		})
	}
}

func TestNamespacePublicCallPreservesCallerContext(t *testing.T) {
	type callerKey struct{}
	for _, mode := range []string{"caller", "shutdown"} {
		t.Run(mode, func(t *testing.T) {
			a := &InstanceNamespaceTransactions{}
			deadline := time.Now().Add(time.Minute)
			caller, cancel := context.WithDeadline(context.WithValue(t.Context(), callerKey{}, "value"), deadline)
			defer cancel()
			call, finish, err := a.beginNamespaceCall(caller)
			if err != nil {
				t.Fatal(err)
			}
			defer finish()
			got, ok := call.Deadline()
			if !ok || !got.Equal(deadline) || call.Value(callerKey{}) != "value" {
				t.Fatal("public context lost caller values or deadline")
			}
			if mode == "caller" {
				cancel()
			} else {
				closing, stop := context.WithCancel(t.Context())
				stop()
				if err := a.Shutdown(closing); !errors.Is(err, currentauthority.ErrBusy) {
					t.Fatal(err)
				}
			}
			select {
			case <-call.Done():
			case <-time.After(time.Second):
				t.Fatal("public context did not cancel")
			}
			finish()
			finish()
			if err := a.Shutdown(t.Context()); err != nil || !a.Drained() {
				t.Fatalf("public context did not drain: %v", err)
			}
		})
	}
}
