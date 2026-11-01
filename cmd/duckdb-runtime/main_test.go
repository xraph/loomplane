package main

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSupervisorChild(_ *testing.T) {
	mode := os.Getenv("LOOMPLANE_TEST_CHILD")
	if mode == "" {
		return
	}
	if mode == "exit" {
		os.Exit(2)
	}
	if mode == "hang" {
		time.Sleep(time.Hour)
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		if mode == "closed-stdin" {
			_ = os.Stdin.Close()
			time.Sleep(time.Hour)
			return
		}
		if scanner.Text() == ".quit" {
			os.Exit(0)
		}
	}
	os.Exit(3)
}

func TestSupervisorOwnsStdinAndQuitsBeforeForcedShutdown(t *testing.T) {
	for _, mode := range []string{"quit", "exit", "hang", "closed-stdin"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			child := exec.CommandContext(context.WithoutCancel(ctx), os.Args[0], "-test.run=^TestSupervisorChild$")
			// Race binaries delay a successful exit by one second. This CLI
			// fixture needs normal exit timing within the unchanged grace.
			child.Env = append(os.Environ(), "LOOMPLANE_TEST_CHILD="+mode, "GORACE="+os.Getenv("GORACE")+" atexit_sleep_ms=0")
			done := make(chan error, 1)
			go func() { done <- serveProcess(ctx, child, "startup contains secret\n", 250*time.Millisecond) }()
			if mode != "exit" {
				select {
				case err := <-done:
					t.Fatalf("stdin was not kept open: %v", err)
				case <-time.After(100 * time.Millisecond):
				}
				cancel()
			}
			select {
			case err := <-done:
				if mode == "closed-stdin" && !errors.Is(err, syscall.EPIPE) {
					t.Fatalf("lost failed quit delivery: %v", err)
				}
				if (mode == "quit") != (err == nil) {
					t.Fatalf("mode %s: %v", mode, err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("supervisor failed to reap child")
			}
		})
	}
}

func TestTokenFileRequiresExactGeneratedHexValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	for _, value := range []string{strings.Repeat("a", 64), "", strings.Repeat("a", 63), strings.Repeat("z", 64), strings.Repeat("a", 64) + "\n", "'query'"} {
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := readToken(path)
		if (value == strings.Repeat("a", 64)) != (err == nil) {
			t.Fatalf("token accepted=%t", err == nil)
		}
		if err == nil && got != value {
			t.Fatal("token was changed")
		}
	}
}

func TestSupervisorReapsChildAfterStartupWriteFailure(t *testing.T) {
	child := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestSupervisorChild$")
	child.Env = append(os.Environ(), "LOOMPLANE_TEST_CHILD=exit")
	err := serveProcess(t.Context(), child, strings.Repeat("startup", 1<<20), time.Second)
	if err == nil || !strings.Contains(err.Error(), "startup failed") {
		t.Fatalf("startup failure was lost: %v", err)
	}
	if child.ProcessState == nil {
		t.Fatal("failed child was not reaped")
	}
}

func TestRuntimeModeFailureReturnsBeforeMainExits(t *testing.T) {
	original := os.Args
	os.Args = []string{"loomplane-duckdb", "unknown-mode"}
	t.Cleanup(func() { os.Args = original })
	if err := run(); err == nil {
		t.Fatal("invalid runtime mode accepted")
	}
}
