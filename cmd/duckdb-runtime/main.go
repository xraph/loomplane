package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const bootstrap = "SET extension_directories=['/opt/duckdb/extensions']; LOAD httpfs; LOAD quack;\n"

func main() {
	if err := run(); err != nil {
		log.Print("DuckDB runtime failed")
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	switch {
	case len(os.Args) == 2 && os.Args[1] == "check":
		return check(ctx)
	case len(os.Args) == 2 && os.Args[1] == "serve-readonly":
		return serve(ctx, true)
	case len(os.Args) == 1:
		return serve(ctx, false)
	default:
		return errors.New("invalid mode")
	}
}

func token() (string, error) { return readToken("/etc/loomplane/token") }
func readToken(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	value := string(raw)
	if len(value) != 64 {
		return "", errors.New("invalid token")
	}
	if _, decodeErr := hex.DecodeString(value); decodeErr != nil {
		return "", errors.New("invalid token")
	}
	return value, nil
}
func check(ctx context.Context) error {
	secret, err := token()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/duckdb", "-batch", "-bail", "-csv", "-noheader")
	cmd.Stdin = strings.NewReader(bootstrap + "SET enable_server_cert_verification=true; SET ca_cert_file='/etc/loomplane/ca.pem'; SELECT * FROM quack_query('quack:localhost:9494', 'SELECT 42', token := '" + secret + "', disable_ssl := false);\n.quit\n")
	var out bytes.Buffer
	cmd.Stdout = &out
	if runErr := cmd.Run(); runErr != nil {
		return errors.New("authenticated readiness failed")
	}
	if strings.TrimSpace(out.String()) != "42" {
		return errors.New("unexpected readiness reply")
	}
	return nil
}
func serve(ctx context.Context, readonly bool) error {
	secret, err := token()
	if err != nil {
		return err
	}
	if readonly {
		if checkErr := validateReadonlyDatabase("/data/analytics.db"); checkErr != nil {
			return checkErr
		}
	}
	// The supervisor delivers .quit before enforcing its own bounded shutdown.
	child := serveCommand(context.WithoutCancel(ctx), readonly)
	return serveProcess(ctx, child, startupSQL(secret, readonly), 20*time.Second)
}

func validateReadonlyDatabase(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("recovery database is missing or invalid")
	}
	return nil
}

func startupSQL(secret string, readonly bool) string {
	listener := "quack_serve('quack:0.0.0.0:9494', allow_other_hostname := true, token := '" + secret + "', disable_ssl := false, ssl_cert_file := '/etc/loomplane/node.pem', ssl_key_file := '/etc/loomplane/node-key.pem')"
	if !readonly {
		return bootstrap + "CALL " + listener + ";\n"
	}
	return bootstrap + "PREPARE loomplane_readonly_listener AS SELECT * FROM " + listener + ";\n" +
		"SET temp_directory=''; SET enable_external_access=false; SET lock_configuration=true;\n" +
		"EXECUTE loomplane_readonly_listener; DEALLOCATE loomplane_readonly_listener;\n"
}

func serveProcess(ctx context.Context, child *exec.Cmd, startup string, grace time.Duration) error {
	child.Stdout, child.Stderr = io.Discard, io.Discard
	stdin, err := child.StdinPipe()
	if err != nil {
		return err
	}
	defer stdin.Close()
	if startErr := child.Start(); startErr != nil {
		return startErr
	}
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	if _, writeErr := io.WriteString(stdin, startup); writeErr != nil {
		killErr := child.Process.Kill()
		waitErr := <-done
		return errors.Join(errors.New("startup failed"), writeErr, killErr, waitErr)
	}
	select {
	case <-done:
		return errors.New("server exited unexpectedly")
	case <-ctx.Done():
	}
	_, quitErr := io.WriteString(stdin, ".quit\n")
	_ = stdin.Close()
	select {
	case waitErr := <-done:
		return errors.Join(quitErr, waitErr)
	case <-time.After(grace):
		killErr := child.Process.Kill()
		waitErr := <-done
		return errors.Join(errors.New("shutdown timed out"), quitErr, killErr, waitErr)
	}
}

func serveCommand(ctx context.Context, readonly bool) *exec.Cmd {
	if readonly {
		return exec.CommandContext(ctx, "/duckdb", "-batch", "-bail", "-readonly", "/data/analytics.db")
	}
	return exec.CommandContext(ctx, "/duckdb", "-batch", "-bail", "/data/analytics.db")
}
