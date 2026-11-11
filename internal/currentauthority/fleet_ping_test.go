package currentauthority

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Exercise the real pool's detached idle ping against a server that accepts
// startup but never answers the ping. Cancellation must release acquisition,
// and the inherited deadline must eventually release pool ownership.
func TestFleetPoolCancelledIdlePingDrains(t *testing.T) {
	var listenConfig net.ListenConfig
	listener, listenErr := listenConfig.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if listenErr != nil {
		t.Fatal(listenErr)
	}
	defer listener.Close()
	ping := make(chan struct{})
	serverDone := make(chan error, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverDone <- acceptErr
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		server := pgproto3.NewBackend(conn, conn)
		if _, err := server.ReceiveStartupMessage(); err != nil {
			serverDone <- err
			return
		}
		server.Send(&pgproto3.AuthenticationOk{})
		server.Send(&pgproto3.BackendKeyData{ProcessID: 1, SecretKey: []byte{0, 0, 0, 1}})
		server.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
		if err := server.Flush(); err != nil {
			serverDone <- err
			return
		}
		if _, err := server.Receive(); err != nil {
			serverDone <- err
			return
		}
		close(ping)
		// The next receive blocks until the client closes the expired connection.
		_, receiveErr := server.Receive()
		serverDone <- receiveErr
	}()
	config, err := pgxpool.ParseConfig("postgres://user@" + listener.Addr().String() + "/db?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	config = writerConfig(config)
	var shouldPing atomic.Bool
	config.ShouldPing = func(context.Context, pgxpool.ShouldPingParams) bool { return shouldPing.Load() }
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	initial, err := pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	initial.Release()
	// PostgreSQL receives cancellation on a separate connection. A healthy
	// cancellation channel closes after reading that startup message.
	cancelDone := make(chan struct{})
	go func() {
		defer close(cancelDone)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		server := pgproto3.NewBackend(conn, conn)
		_, _ = server.ReceiveStartupMessage()
	}()

	shouldPing.Store(true)
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	acquired := make(chan error, 1)
	go func() {
		conn, err := pool.Acquire(ctx)
		if conn != nil {
			conn.Release()
		}
		acquired <- err
	}()
	select {
	case <-ping:
	case <-time.After(time.Second):
		t.Fatal("idle ping never reached server")
	}
	cancel()
	select {
	case err := <-acquired:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("acquire=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled acquisition did not return")
	}
	closed := make(chan struct{})
	go func() { pool.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("detached ping retained pool beyond original deadline")
	}
	select {
	case <-cancelDone:
	case <-time.After(time.Second):
		t.Fatal("cancel connection retained")
	}
	select {
	case <-serverDone:
	case <-time.After(time.Second):
		t.Fatal("server connection retained")
	}
}
