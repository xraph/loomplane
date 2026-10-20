//go:build integration

package mysql

import (
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
	if os.Getenv("LOOMPLANE_TEST_DOCKER_SOCKET") == "" {
		t.Skip("set LOOMPLANE_TEST_DOCKER_SOCKET for real MySQL/MariaDB recovery")
	}
	for _, engine := range []string{"mysql", "mariadb"} {
		t.Run(engine, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 6*time.Minute)
			defer cancel()
			docker := func(args ...string) string {
				t.Helper()
				command := exec.CommandContext(ctx, "docker", append([]string{"--host", "unix://" + os.Getenv("LOOMPLANE_TEST_DOCKER_SOCKET")}, args...)...)
				out, err := command.CombinedOutput()
				if err != nil {
					t.Fatalf("Docker native acceptance failed: %v\n%s", err, out)
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
			version := "8.4"
			if engine == "mariadb" {
				version = "11.8"
			}
			recipe, lookupErr := recipes.Lookup(engine, version, architecture)
			if lookupErr != nil {
				t.Fatal(lookupErr)
			}
			docker("pull", recipe.Image)
			password := rand.Text() + rand.Text()
			prefix := "loomplane-native-" + strings.ToLower(rand.Text()[:10])
			start := func(suffix string) Database {
				t.Helper()
				name, volume := prefix+suffix, prefix+suffix+"-data"
				t.Logf("native fixture %s", name)
				docker("volume", "create", volume)
				t.Cleanup(func() {
					cleanup, done := context.WithTimeout(context.Background(), 30*time.Second)
					defer done()
					for _, args := range [][]string{{"rm", "-f", name}, {"volume", "rm", volume}} {
						out, err := exec.CommandContext(cleanup, "docker", append([]string{"--host", "unix://" + os.Getenv("LOOMPLANE_TEST_DOCKER_SOCKET")}, args...)...).CombinedOutput()
						if err != nil {
							t.Errorf("fixture cleanup: %v %s", err, out)
						}
					}
				})
				runArgs := make([]string, 0, 32)
				runArgs = append(runArgs, "run", "--detach", "--name", name, "--network", "none", "--mount", "type=volume,source="+volume+",target=/var/lib/mysql", "--env", "MYSQL_DATABASE=app_db", "--env", "MYSQL_ROOT_PASSWORD="+password, "--env", "MARIADB_DATABASE=app_db", "--env", "MARIADB_ROOT_PASSWORD="+password, "--env", "LOOMPLANE_APP_PASSWORD="+password, recipe.Image)
				docker(append(runArgs, recipe.RuntimeArgs(suffix == "-copy")...)...)
				db := Database{Engine: engine, Name: "app_db", Recovery: suffix == "-copy", Run: func(ctx context.Context, args []string, out io.Writer) error {
					command := exec.CommandContext(ctx, "docker", append([]string{"--host", "unix://" + os.Getenv("LOOMPLANE_TEST_DOCKER_SOCKET"), "exec", name}, args...)...)
					command.Stdout = out
					return command.Run()
				}, Upload: func(ctx context.Context, path string, size int64, write func(io.Writer) error) error {
					local := filepath.Join(t.TempDir(), path)
					file, err := os.OpenFile(local, os.O_CREATE|os.O_WRONLY, 0o600)
					if err != nil {
						return err
					}
					writeErr := write(file)
					closeErr := file.Close()
					if writeErr != nil {
						return writeErr
					}
					if closeErr != nil {
						return closeErr
					}
					info, err := os.Stat(local)
					if err != nil {
						return err
					}
					if info.Size() != size {
						return fmt.Errorf("uploaded size %d differs from %d", info.Size(), size)
					}
					out, err := exec.CommandContext(ctx, "docker", "--host", "unix://"+os.Getenv("LOOMPLANE_TEST_DOCKER_SOCKET"), "cp", local, name+":/tmp/"+path).CombinedOutput()
					if err != nil {
						return fmt.Errorf("upload: %w %s", err, out)
					}
					return nil
				}}
				for db.Ready(ctx) != nil {
					select {
					case <-ctx.Done():
						t.Fatal("database did not become ready")
					case <-time.After(time.Second):
					}
				}
				if err := db.Configure(ctx, password); err != nil {
					t.Fatal(err)
				}
				return db
			}
			source := start("-source")
			actualVersion, versionErr := source.query(ctx, "SELECT VERSION()")
			if versionErr != nil || !strings.HasPrefix(actualVersion, recipe.ProductVersion) {
				t.Fatalf("native version: %s %v", actualVersion, versionErr)
			}
			query := func(db Database, sql string) string {
				t.Helper()
				var out strings.Builder
				if err := db.Run(ctx, db.command(true, "", db.client(), "--protocol=tcp", "--host=127.0.0.1", "--user=app", "--batch", "--skip-column-names", "--database", "app_db", "--execute", sql), &out); err != nil {
					t.Fatal(err)
				}
				return strings.TrimSpace(out.String())
			}
			query(source, "CREATE TABLE probe(value text) ENGINE=InnoDB; INSERT INTO probe VALUES ('native-recovery'); CREATE VIEW probe_view AS SELECT value FROM probe")
			if err := source.Run(ctx, source.command(true, "", source.client(), "--user=app", "--execute", "CREATE DATABASE appXdb"), io.Discard); err == nil {
				t.Fatal("application grant escaped its database through underscore wildcard")
			}
			query(source, "CREATE FUNCTION identity_value(s TEXT) RETURNS TEXT DETERMINISTIC RETURN s; CREATE PROCEDURE read_probe() SELECT value FROM probe_view; CREATE TRIGGER retain_value BEFORE INSERT ON probe FOR EACH ROW SET NEW.value=CONCAT(NEW.value,''); CREATE EVENT preserve_probe ON SCHEDULE EVERY 1 HOUR STARTS CURRENT_TIMESTAMP + INTERVAL 1 HOUR DO UPDATE probe SET value='timer-fired'")
			if err := source.Run(ctx, source.command(true, "", source.client(), "--user=app", "--execute", "SELECT * FROM mysql.user"), io.Discard); err == nil {
				t.Fatal("application role can read global users")
			}
			docker("restart", prefix+"-source")
			for source.Ready(ctx) != nil {
				select {
				case <-ctx.Done():
					t.Fatal("restart did not become ready")
				case <-time.After(time.Second):
				}
			}
			if got := query(source, "SELECT value FROM probe_view"); got != "native-recovery" {
				t.Fatalf("restart lost data: %s", got)
			}
			files := testFiles(t)
			object, err := source.Export(ctx, files, "native.sql")
			if err != nil {
				t.Fatal(err)
			}
			query(source, "DELETE FROM probe")
			restored := start("-copy")
			if err := restored.Restore(ctx, files, object); err != nil {
				t.Fatal(err)
			}
			if got := query(restored, "SELECT identity_value(value) FROM probe_view"); got != "native-recovery" {
				t.Fatalf("restored data: %s", got)
			}
			if got := query(restored, "CALL read_probe()"); got != "native-recovery" {
				t.Fatalf("restored procedure: %s", got)
			}
			if got := query(restored, "SELECT @@event_scheduler"); got != "DISABLED" {
				t.Fatalf("recovery can execute scheduled events: %s", got)
			}
			if got := query(restored, "SELECT COUNT(*) FROM information_schema.triggers WHERE trigger_schema='app_db'"); got != "1" {
				t.Fatalf("restored trigger: %s", got)
			}
			if got := query(restored, "SELECT COUNT(*) FROM information_schema.events WHERE event_schema='app_db'"); got != "1" {
				t.Fatalf("restored event: %s", got)
			}
			if got := query(source, "SELECT count(*) FROM probe"); got != "0" {
				t.Fatalf("restore changed source: %s", got)
			}
			object.SHA256 = strings.Repeat("0", 64)
			if err := restored.Restore(ctx, files, object); err == nil {
				t.Fatal("corrupt backup accepted")
			}
			if got := query(restored, "SELECT count(*) FROM probe"); got != "1" {
				t.Fatalf("corrupt import modified the copy: %s", got)
			}
			t.Logf("%s %s: restricted write, restart, native export, isolated restore and corruption refusal passed", engine, recipe.ProductVersion)
		})
	}
}
