//go:build integration

package redis

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/recipes"
)

func TestNativeRecovery(t *testing.T) {
	socket := os.Getenv("LOOMPLANE_TEST_DOCKER_SOCKET")
	if socket == "" {
		t.Skip("set LOOMPLANE_TEST_DOCKER_SOCKET for native Redis/Valkey recovery")
	}
	for _, engine := range []string{"redis", "valkey"} {
		t.Run(engine, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
			defer cancel()
			docker := func(args ...string) string {
				t.Helper()
				out, err := exec.CommandContext(ctx, "docker", append([]string{"--host", "unix://" + socket}, args...)...).CombinedOutput()
				if err != nil {
					t.Fatalf("native cache fixture: %v %s", err, out)
				}
				return strings.TrimSpace(string(out))
			}
			architecture := docker("info", "--format", "{{.Architecture}}")
			if architecture == "aarch64" {
				architecture = "arm64"
			}
			if architecture == "x86_64" {
				architecture = "amd64"
			}
			recipe, err := recipes.Lookup(engine, recipes.DefaultVersion(engine), architecture)
			if err != nil {
				t.Fatal(err)
			}
			prefix := "loomplane-native-cache-" + strings.ToLower(rand.Text()[:10])
			start := func(suffix string) Database {
				t.Helper()
				name, volume := prefix+suffix, prefix+suffix+"-data"
				admin, password := rand.Text()+rand.Text(), rand.Text()+rand.Text()
				docker("volume", "create", volume)
				t.Cleanup(func() {
					cleanup, done := context.WithTimeout(context.Background(), 30*time.Second)
					defer done()
					for _, args := range [][]string{{"rm", "-f", name}, {"volume", "rm", volume}} {
						if out, cleanupErr := exec.CommandContext(cleanup, "docker", append([]string{"--host", "unix://" + socket}, args...)...).CombinedOutput(); cleanupErr != nil {
							t.Errorf("cleanup: %v %s", cleanupErr, out)
						}
					}
				})
				args := make([]string, 0, 16)
				args = append(args, "run", "--detach", "--init", "--name", name, "--network", "none", "--mount", "type=volume,source="+volume+",target=/data", "--env", "LOOMPLANE_CACHE_ADMIN_PASSWORD="+admin, "--env", "LOOMPLANE_APP_PASSWORD="+password, recipe.Image)
				args = append(args, recipe.RuntimeArgs(suffix == "-copy")...)
				docker(args...)
				db := Database{Engine: engine, Recovery: suffix == "-copy", Run: func(ctx context.Context, args []string, out io.Writer) error {
					if strings.Contains(strings.Join(args, " "), password) {
						t.Fatal("credential in native arguments")
					}
					cmd := exec.CommandContext(ctx, "docker", append([]string{"--host", "unix://" + socket, "exec", name}, args...)...)
					cmd.Stdout = out
					var diagnostic bytes.Buffer
					cmd.Stderr = &diagnostic
					cmd.WaitDelay = 5 * time.Second
					if runErr := cmd.Run(); runErr != nil {
						return fmt.Errorf("native cache: %w: %s", runErr, diagnostic.String())
					}
					return nil
				}, Upload: func(ctx context.Context, path string, size int64, write func(io.Writer) error) error {
					local := filepath.Join(t.TempDir(), path)
					var content bytes.Buffer
					if writeErr := write(&content); writeErr != nil {
						return writeErr
					}
					if int64(content.Len()) != size {
						return fmt.Errorf("wrong upload size")
					}
					if writeErr := os.WriteFile(local, content.Bytes(), 0600); writeErr != nil {
						return writeErr
					}
					out, copyErr := exec.CommandContext(ctx, "docker", "--host", "unix://"+socket, "cp", local, name+":/tmp/"+path).CombinedOutput()
					if copyErr != nil {
						return fmt.Errorf("upload: %w %s", copyErr, out)
					}
					return nil
				}}
				wait := func(ctx context.Context) error {
					for {
						if readyErr := db.Ready(ctx); readyErr == nil {
							return nil
						}
						select {
						case <-ctx.Done():
							return ctx.Err()
						case <-time.After(200 * time.Millisecond):
						}
					}
				}
				db.Restart = func(ctx context.Context) error { docker("restart", "-t", "10", name); return wait(ctx) }
				if readyErr := wait(ctx); readyErr != nil {
					t.Fatal(readyErr)
				}
				if configureErr := db.Configure(ctx, password); configureErr != nil {
					t.Fatal(configureErr)
				}
				if configureErr := db.Configure(ctx, rand.Text()+rand.Text()); configureErr == nil {
					t.Fatal("wrong application password accepted")
				}
				if unauthErr := db.Run(ctx, []string{engine + "-cli", "-e", "PING"}, io.Discard); unauthErr == nil {
					t.Fatal("unauthenticated client accepted")
				}
				return db
			}
			source := start("-source")
			query := func(db Database, args ...string) string {
				t.Helper()
				value, queryErr := db.query(ctx, true, "", args...)
				if queryErr != nil {
					t.Fatal(queryErr)
				}
				return value
			}
			query(source, "SET", "probe", "retained")
			query(source, "HSET", "hash", "field", "value")
			query(source, "RPUSH", "list", "one", "two")
			query(source, "XADD", "stream", "1-0", "field", "value")
			query(source, "SET", "expiring", "value", "EX", "300")
			for _, args := range [][]string{{"CONFIG", "GET", "dir"}, {"ACL", "LIST"}, {"FLUSHALL"}, {"REPLICAOF", "127.0.0.1", "1"}, {"MODULE", "LIST"}, {"SAVE"}, {"DEBUG", "RELOAD"}} {
				if _, denied := source.query(ctx, true, "", args...); denied == nil {
					t.Fatalf("app allowed administration: %v", args)
				}
			}
			if err = source.Restart(ctx); err != nil {
				t.Fatal(err)
			}
			if got := query(source, "GET", "probe"); got != "retained" {
				t.Fatalf("AOF restart lost value: %s", got)
			}
			files := testFiles(t)
			object, err := source.Export(ctx, files, "native.rdb")
			if err != nil {
				t.Fatal(err)
			}
			query(source, "DEL", "probe")
			recovered := start("-copy")
			if err = recovered.Restore(ctx, files, object); err != nil {
				t.Fatal(err)
			}
			if got := query(recovered, "GET", "probe"); got != "retained" {
				t.Fatalf("restored value: %s", got)
			}
			if query(recovered, "HGET", "hash", "field") != "value" || query(recovered, "LRANGE", "list", "0", "-1") != "one\ntwo" || query(recovered, "XLEN", "stream") != "1" {
				t.Fatal("RDB lost native data types")
			}
			if query(recovered, "EXISTS", "expiring") != "1" || query(source, "GET", "probe") != "" {
				t.Fatal("restore lost TTL key or changed source")
			}
			if _, denied := recovered.query(ctx, true, "", "SET", "probe", "changed"); denied == nil {
				t.Fatal("drill account can modify recovered data")
			}
			if err = recovered.Restart(ctx); err != nil {
				t.Fatal(err)
			}
			if query(recovered, "GET", "probe") != "retained" {
				t.Fatal("recovery restart replaced RDB with empty data")
			}
			if err = recovered.Restore(ctx, files, object); err == nil {
				t.Fatal("reimport overwrote a populated copy")
			}
			object.SHA256 = strings.Repeat("0", 64)
			if err = recovered.Restore(ctx, files, object); err == nil {
				t.Fatal("corrupt snapshot accepted")
			}
			if query(recovered, "GET", "probe") != "retained" {
				t.Fatal("corrupt restore modified the copy")
			}
			t.Logf("%s %s: auth, restricted app, AOF restart, RDB types, isolated read-only restore, restart and integrity passed", engine, recipe.ProductVersion)
		})
	}
}
