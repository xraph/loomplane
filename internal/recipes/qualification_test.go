//go:build qualification

package recipes_test

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xraph/trove"
	"github.com/xraph/trove/drivers/localdriver"

	"github.com/xraph/loomplane/internal/backupfiles"
	nativepg "github.com/xraph/loomplane/internal/clouds/postgres"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/fabriqloom"
	"github.com/xraph/loomplane/internal/recipes"
)

// TestQualifyNewEngineImages starts the pinned TimescaleDB, FalkorDB and
// Elasticsearch images on the local Docker daemon, the way a server target
// runs them, and proves the recipes' paths, users and security settings.
// It pulls public images, so it runs only on request:
//
//	go test -tags=qualification ./internal/recipes/ -run TestQualifyNewEngineImages -count=1 -timeout 20m
func TestQualifyNewEngineImages(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not installed")
	}
	q := &qualifier{t: t, arch: runtime.GOARCH, prefix: "lp-qual-" + strings.ToLower(rand.Text()[:8])}
	if q.arch != "amd64" && q.arch != "arm64" {
		t.Fatalf("no pinned images for %s", q.arch)
	}
	// Each subtest removes its containers and volumes before the next starts.
	t.Run("timescaledb", func(t *testing.T) { q.sub(t).timescaledb(t) })
	t.Run("falkordb", func(t *testing.T) { q.sub(t).falkordb(t) })
	t.Run("elasticsearch", func(t *testing.T) { q.sub(t).elasticsearch(t) })
}

type qualifier struct {
	t      *testing.T
	arch   string
	prefix string
}

// sub is the qualifier for one subtest.
func (q *qualifier) sub(t *testing.T) *qualifier {
	sub := *q
	sub.t = t
	return &sub
}

// docker runs the docker CLI and returns stdout.
func docker(ctx context.Context, stdin io.Reader, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("docker %s: %w: %s", strings.Join(args[:min(len(args), 2)], " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func (q *qualifier) pull(image string) {
	q.t.Helper()
	ctx, cancel := context.WithTimeout(q.t.Context(), 15*time.Minute)
	defer cancel()
	if out, err := docker(ctx, nil, "pull", "--quiet", image); err != nil {
		q.t.Fatalf("%v\n%s", err, out)
	}
}

// container is a started container; its volumes and the container itself
// are removed when the test ends.
type container struct {
	q    *qualifier
	name string
}

func (q *qualifier) remove(name string, volumes ...string) {
	q.t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if out, err := docker(ctx, nil, "rm", "-f", "-v", name); err != nil {
			q.t.Errorf("remove container %s: %v\n%s", name, err, out)
		}
		for _, volume := range volumes {
			if out, err := docker(ctx, nil, "volume", "rm", "-f", volume); err != nil {
				q.t.Errorf("remove volume %s: %v\n%s", volume, err, out)
			}
		}
	})
}

// create makes a container (not started) with a named volume at mount.
func (q *qualifier) create(name, mount string, args ...string) *container {
	q.t.Helper()
	ctx, cancel := context.WithTimeout(q.t.Context(), 2*time.Minute)
	defer cancel()
	volume := name + "-data"
	q.remove(name, volume)
	if out, err := docker(ctx, nil, "volume", "create", volume); err != nil {
		q.t.Fatalf("%v\n%s", err, out)
	}
	all := append([]string{"create", "--name", name, "--init", "-v", volume + ":" + mount}, args...)
	if out, err := docker(ctx, nil, all...); err != nil {
		q.t.Fatalf("%v\n%s", err, out)
	}
	return &container{q: q, name: name}
}

func (c *container) start() {
	c.q.t.Helper()
	ctx, cancel := context.WithTimeout(c.q.t.Context(), time.Minute)
	defer cancel()
	if out, err := docker(ctx, nil, "start", c.name); err != nil {
		c.q.t.Fatalf("%v\n%s", err, out)
	}
}

// run is a database tool exec, as the runtime runs one: as the image's own
// user, with stdout apart from diagnostics.
func (c *container) run(ctx context.Context, args []string, out io.Writer) error {
	cmd := exec.CommandContext(ctx, "docker", append([]string{"exec", c.name}, args...)...)
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = out, &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// output runs a command and returns its stdout.
func (c *container) output(ctx context.Context, args ...string) (string, error) {
	var out bytes.Buffer
	err := c.run(ctx, args, &out)
	return out.String(), err
}

// owner is who the container's tools run as, which the server runtime asks
// for the same way (server.execOwner) before it uploads a database file.
func (c *container) owner(ctx context.Context) (uid, gid int, err error) {
	for flag, dst := range map[string]*int{"-u": &uid, "-g": &gid} {
		out, err := c.output(ctx, "id", flag)
		if err != nil {
			return 0, 0, err
		}
		if *dst, err = strconv.Atoi(strings.TrimSpace(out)); err != nil {
			return 0, 0, err
		}
	}
	return uid, gid, nil
}

// put copies a file in through Docker's archive API, as the runtime does.
// Docker extracts it as the owner in the header, so a database file is
// owned by the user the tools run as, or an image that isn't root could
// neither read it nor remove it from the sticky /tmp.
func (c *container) put(ctx context.Context, dir, name string, mode int64, uid, gid int, size int64, write func(io.Writer) error) error {
	reader, writer := io.Pipe()
	go func() {
		archive := tar.NewWriter(writer)
		err := archive.WriteHeader(&tar.Header{Name: name, Mode: mode, Uid: uid, Gid: gid, Size: size, Typeflag: tar.TypeReg})
		if err == nil {
			err = write(archive)
		}
		if err == nil {
			err = archive.Close()
		}
		writer.CloseWithError(err)
	}()
	_, err := docker(ctx, reader, "cp", "-", c.name+":"+dir)
	reader.CloseWithError(err)
	return err
}

// postgres starts the recipe's image the way a server target does.
func (q *qualifier) postgres(recipe *recipes.Recipe, name, password string) *container {
	q.t.Helper()
	c := q.create(q.prefix+"-"+name, recipe.MountPath, "-e", "POSTGRES_DB=fabriq", "-e", "POSTGRES_PASSWORD="+password, recipe.Image)
	c.start()
	return c
}

func (c *container) database(recovery bool, extensions []string) nativepg.Database {
	return nativepg.Database{
		Name: "fabriq", Recovery: recovery, Extensions: extensions, Run: c.run,
		Upload: func(ctx context.Context, name string, size int64, write func(io.Writer) error) error {
			uid, gid, err := c.owner(ctx)
			if err != nil {
				return err
			}
			return c.put(ctx, "/tmp", name, 0o600, uid, gid, size, write)
		},
	}
}

// waitFor retries check until it passes or the deadline.
func waitFor(t *testing.T, what string, within time.Duration, check func(context.Context) error) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		err := check(ctx)
		cancel()
		if err == nil {
			return
		}
		if time.Now().After(deadline) || t.Context().Err() != nil {
			t.Fatalf("%s not ready after %s: %v", what, within, err)
		}
		time.Sleep(2 * time.Second)
	}
}

func (c *container) psql(ctx context.Context, sql string) (string, error) {
	return c.output(ctx, "psql", "-U", "postgres", "-d", "fabriq", "-At", "-v", "ON_ERROR_STOP=1", "-c", sql)
}

// psqlAsApp runs one statement as the application role over TCP, the way
// Fabriq connects.
func (c *container) psqlAsApp(ctx context.Context, password, sql string) (string, error) {
	return c.output(ctx, "env", "PGPASSWORD="+password, "psql", "-h", "127.0.0.1", "-U", "app", "-d", "fabriq", "-At", "-v", "ON_ERROR_STOP=1", "-c", sql)
}

func (q *qualifier) timescaledb(t *testing.T) {
	recipe, err := recipes.Default().LookupEngine("timescaledb", "16", q.arch, "server")
	if err != nil {
		t.Fatal(err)
	}
	q.pull(recipe.Image)
	password, appPassword := rand.Text(), rand.Text()

	source := q.postgres(recipe, "ts-source", password)
	db := source.database(false, nil)
	waitFor(t, "source database", 5*time.Minute, func(ctx context.Context) error { return db.Ready(ctx) })

	// The runtime's tools run with no host and no socket directory of their
	// own, so they must find the server's default socket.
	var out string
	if out, err = source.output(t.Context(), "pg_isready", "-h", "127.0.0.1", "-U", "postgres", "-d", "fabriq"); err != nil {
		t.Fatalf("pg_isready: %v %s", err, out)
	}
	if out, err = source.output(t.Context(), "psql", "-U", "postgres", "-d", "fabriq", "-At", "-c", "SELECT 1"); err != nil || out != "1\n" {
		t.Fatalf("psql over the default socket: %v %q", err, out)
	}
	if out, err = source.psql(t.Context(), "SHOW shared_preload_libraries"); err != nil || !strings.Contains(out, "timescaledb") {
		t.Fatalf("timescaledb is not preloaded: %v %q", err, out)
	}

	if err = db.Configure(t.Context(), appPassword); err != nil {
		t.Fatalf("configure: %v", err)
	}
	// The extensions are created as postgres, as the app role cannot. Fabriq
	// then creates its own objects as app, so the tables are app's.
	for _, statement := range []string{
		"CREATE EXTENSION IF NOT EXISTS timescaledb",
		"CREATE EXTENSION IF NOT EXISTS vector",
		"CREATE EXTENSION IF NOT EXISTS postgis",
		// Fabriq creates the toolkit when the image has it. A restore keeps
		// only the extension entries it did not pre-create, so the recipe
		// must list it.
		"CREATE EXTENSION IF NOT EXISTS timescaledb_toolkit",
	} {
		if out, err = source.psql(t.Context(), statement); err != nil {
			t.Fatalf("%s: %v %s", statement, err, out)
		}
	}
	for _, statement := range []string{
		"CREATE TABLE items (id int PRIMARY KEY, embedding vector(3))",
		"INSERT INTO items VALUES (1, '[1,2,3]'), (2, '[4,5,6]')",
		"CREATE TABLE places (id int PRIMARY KEY, geom geometry(Point, 4326))",
		"INSERT INTO places VALUES (1, ST_SetSRID(ST_MakePoint(-87.62, 41.88), 4326))",
		"CREATE TABLE readings (at timestamptz NOT NULL, value double precision)",
		"SELECT create_hypertable('readings', 'at', chunk_time_interval => interval '1 day')",
		"INSERT INTO readings SELECT now() - make_interval(hours => n * 12), n FROM generate_series(1, 48) n",
	} {
		if out, err = source.psqlAsApp(t.Context(), appPassword, statement); err != nil {
			t.Fatalf("as app: %s: %v %s", statement, err, out)
		}
	}

	driver := localdriver.New()
	dsn := "file://" + t.TempDir()
	if err = driver.Open(t.Context(), dsn); err != nil {
		t.Fatal(err)
	}
	files, err := trove.Open(driver)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = files.Close(context.Background()) })
	if err = files.CreateBucket(t.Context(), "backup"); err != nil {
		t.Fatal(err)
	}
	store, err := backupfiles.New(files, "backup", dsn)
	if err != nil {
		t.Fatal(err)
	}
	object, err := db.Export(t.Context(), store, "timescaledb.dump")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	target := q.postgres(recipe, "ts-restore", rand.Text())
	recovery := target.database(true, recipe.Extensions)
	waitFor(t, "recovery database", 5*time.Minute, func(ctx context.Context) error { return recovery.Ready(ctx) })
	if err := recovery.Configure(t.Context(), appPassword); err != nil {
		t.Fatalf("configure recovery: %v", err)
	}
	if err := recovery.Restore(t.Context(), store, object); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if err := recovery.Probe(t.Context()); err != nil {
		t.Fatalf("probe: %v", err)
	}

	for statement, want := range map[string]string{
		"SELECT count(*) FROM items WHERE embedding <-> '[1,2,3]' < 1":       "1\n",
		"SELECT count(*) FROM places WHERE ST_X(geom) < -87":                 "1\n",
		"SELECT count(*) FROM readings":                                      "48\n",
		"SELECT count(*) FROM timescaledb_information.hypertables":           "1\n",
		"SELECT count(*) >= 2 FROM timescaledb_information.chunks":           "t\n",
		"SELECT string_agg(extname, ',' ORDER BY extname) FROM pg_extension": "",
	} {
		out, err := target.psql(t.Context(), statement)
		if err != nil {
			t.Fatalf("%s: %v %s", statement, err, out)
		}
		if want != "" && out != want {
			t.Errorf("%s = %q, want %q", statement, out, want)
		}
		if want == "" {
			for _, extension := range recipe.Extensions {
				if !strings.Contains(out, extension) {
					t.Errorf("restored database lacks extension %s: %s", extension, out)
				}
			}
		}
	}

	// The copy belongs to app, hypertable and chunks included, and app can
	// read it over TCP.
	chunks := "FROM pg_class c WHERE c.relnamespace = '_timescaledb_internal'::regnamespace AND c.relname ~ '^_hyper_' AND c.relkind = 'r'"
	for statement, want := range map[string]string{
		"SELECT count(*) FROM items":    "2\n",
		"SELECT count(*) FROM places":   "1\n",
		"SELECT count(*) FROM readings": "48\n",
		"SELECT string_agg(DISTINCT pg_get_userbyid(relowner), ',') FROM pg_class WHERE relname IN ('items', 'places', 'readings')": "app\n",
		"SELECT count(*) >= 2 " + chunks:                                          "t\n",
		"SELECT count(*) " + chunks + " AND pg_get_userbyid(c.relowner) <> 'app'": "0\n",
	} {
		out, err := target.psqlAsApp(t.Context(), appPassword, statement)
		if err != nil {
			t.Fatalf("as app: %s: %v %s", statement, err, out)
		}
		if out != want {
			t.Errorf("as app: %s = %q, want %q", statement, out, want)
		}
	}
}

// envOf resolves a component's environment, with the generated secret.
func qualificationEnv(c recipes.Component, secret string) []string {
	args := make([]string, 0, 2*len(c.Env))
	for _, v := range c.Env {
		value := v.Value
		if v.Secret != "" {
			value = secret
		}
		args = append(args, "-e", v.Name+"="+value)
	}
	return args
}

func (q *qualifier) falkordb(t *testing.T) {
	composition, err := recipes.Default().Resolve("falkordb", "4.2", q.arch, "server")
	if err != nil {
		t.Fatal(err)
	}
	c := composition.Components[0]
	q.pull(c.Image)
	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	password := hex.EncodeToString(raw)

	// The runtime sets Command as the entrypoint and Args as the command.
	args := append([]string{"-m", fmt.Sprintf("%dm", c.Resources.MemoryMB)}, qualificationEnv(c.Component, password)...)
	args = append(args, "--entrypoint", c.Command[0], c.Image)
	args = append(args, c.Command[1:]...)
	args = append(args, c.Args...)
	graph := q.create(q.prefix+"-falkordb", c.Volume.MountPath, args...)
	graph.start()

	waitFor(t, "FalkorDB", 2*time.Minute, func(ctx context.Context) error {
		reply, pingErr := graph.output(ctx, "redis-cli", "--no-auth-warning", "-a", password, "PING")
		if pingErr != nil || strings.TrimSpace(reply) != "PONG" {
			return fmt.Errorf("%w %q", pingErr, reply)
		}
		return nil
	})
	out, err := graph.output(t.Context(), "redis-cli", "--no-auth-warning", "-a", password, "GRAPH.QUERY", "g", "CREATE (:N)")
	if err != nil || !strings.Contains(out, "Nodes created: 1") {
		t.Fatalf("GRAPH.QUERY: %v %q", err, out)
	}
	out, err = graph.output(t.Context(), "redis-cli", "PING")
	if !strings.Contains(out, "NOAUTH") {
		t.Fatalf("PING without the password answered %q (%v)", out, err)
	}
	if out, err = graph.output(t.Context(), "redis-cli", "--no-auth-warning", "-a", "wrong", "PING"); strings.Contains(out, "PONG") {
		t.Fatalf("a wrong password was accepted: %q (%v)", out, err)
	}
	// The password never reaches argv.
	cmdlines, err := graph.output(t.Context(), "sh", "-c", "cat /proc/[0-9]*/cmdline | tr '\\0' ' '")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ReplaceAll(cmdlines, "-a "+password, ""), password) {
		t.Fatal("the password is in a process's arguments")
	}
}

func (q *qualifier) elasticsearch(t *testing.T) {
	composition, err := recipes.Default().Resolve("elasticsearch", "9.4", q.arch, "server")
	if err != nil {
		t.Fatal(err)
	}
	c := composition.Components[0]
	q.pull(c.Image)
	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	password := hex.EncodeToString(raw)

	args := append([]string{"-m", fmt.Sprintf("%dm", c.Resources.MemoryMB), "-p", "127.0.0.1::9200"}, qualificationEnv(c.Component, password)...)
	args = append(args, c.Image)
	search := q.create(q.prefix+"-elasticsearch", c.Volume.MountPath, args...)

	// The hook renders the file; the runtime copies it in before the start.
	rendered, err := fabriqloom.SearchHooks{}.Files(t.Context(), deployments.ComponentRun{Component: c})
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range c.Files {
		data, ok := rendered[file.Path]
		if !ok {
			t.Fatalf("hook did not render %s", file.Path)
		}
		dir, name := file.Path[:strings.LastIndex(file.Path, "/")], file.Path[strings.LastIndex(file.Path, "/")+1:]
		if err = search.put(t.Context(), dir, name, int64(file.Mode), 0, 0, int64(len(data)), func(w io.Writer) error {
			_, writeErr := w.Write(data)
			return writeErr
		}); err != nil {
			t.Fatal(err)
		}
	}
	search.start()

	out, err := docker(t.Context(), nil, "port", search.name, "9200/tcp")
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + strings.TrimSpace(strings.SplitN(out, "\n", 2)[0])
	send := func(method, path, user, body string) (int, string) {
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Second)
		defer cancel()
		request, err := http.NewRequestWithContext(ctx, method, base+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if user != "" {
			request.SetBasicAuth(user, password)
		}
		if body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			return 0, err.Error()
		}
		defer response.Body.Close()
		out, _ := io.ReadAll(io.LimitReader(response.Body, 1<<16))
		return response.StatusCode, string(out)
	}
	get := func(path, user string) (int, string) { return send(http.MethodGet, path, user, "") }

	// The same unauthenticated request the readiness probe sends.
	waitFor(t, "Elasticsearch", 8*time.Minute, func(context.Context) error {
		if status, body := get("/", ""); status != http.StatusOK {
			return errors.New(fmt.Sprint(status, " ", body))
		}
		return nil
	})
	// The root document answers before the native realm has created its
	// security index, and until then even the right password is refused.
	// Wait for it, and say how long it took.
	began := time.Now()
	waitFor(t, "the elastic user", 3*time.Minute, func(context.Context) error {
		if status, body := get("/_cluster/health", "elastic"); status != http.StatusOK {
			return errors.New(fmt.Sprint(status, " ", body))
		}
		return nil
	})
	t.Logf("the elastic password was accepted %s after the root document answered", time.Since(began).Round(time.Second))
	// An index named like a tenant's, created by the elastic user, must stay
	// invisible to the anonymous role.
	const index = "tenant-acme-projection"
	if status, body := send(http.MethodPut, "/"+index, "elastic", "{}"); status != http.StatusOK {
		t.Fatalf("creating %s as elastic = %d %s", index, status, body)
	}
	if status, body := get("/_cat/indices", "elastic"); status != http.StatusOK || !strings.Contains(body, index) {
		t.Errorf("/_cat/indices as elastic = %d %s, want 200 listing %s", status, body, index)
	}
	// The anonymous role reads the root document and nothing else. In
	// particular no health call may answer, because level=indices and the
	// per-index form both name every index.
	if status, body := get("/", ""); status != http.StatusOK || !strings.Contains(body, "cluster_name") {
		t.Errorf("anonymous / = %d %s, want 200", status, body)
	}
	for _, path := range []string{
		"/_cluster/health",
		"/_cluster/health?level=indices",
		"/_cluster/health/" + index,
		"/_cluster/health/.security-7",
		"/_cat/indices",
		"/" + index + "/_search",
	} {
		if status, body := get(path, ""); status != http.StatusUnauthorized && status != http.StatusForbidden {
			t.Errorf("anonymous %s = %d %s, want 401 or 403", path, status, body)
		}
	}
	if status, body := get("/_cluster/health", "elastic"); status != http.StatusOK {
		t.Errorf("cluster health as elastic = %d %s", status, body)
	}
}
