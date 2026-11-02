// Package rabbitmq backs up a stopped single-node RabbitMQ message store.
package rabbitmq

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"slices"

	"github.com/xraph/loomplane/internal/backupfiles"
	"github.com/xraph/loomplane/internal/clouds/dbexec"
)

const LabelEngine = "loomplane.rabbitmq.engine"
const maxArchive = int64(2 << 30)
const shellPrefix = `set -eu
export HOME=/var/lib/rabbitmq
export RABBITMQ_CTL_ERL_ARGS='+S 1:1'
ctl() { rabbitmqctl -q -t 30 "$@"; }
`

type Database struct {
	Recovery bool
	Run      dbexec.Run
	Upload   dbexec.Upload
}

func command(script string, args ...string) []string {
	return append([]string{"sh", "-c", shellPrefix + script, "loomplane-rabbitmq"}, args...)
}
func (d Database) Ready(ctx context.Context) error {
	return d.Run(ctx, command(`ctl await_startup`), io.Discard)
}
func (d Database) Probe(ctx context.Context) error {
	return d.Run(ctx, command(`ctl authenticate_user app "$LOOMPLANE_APP_PASSWORD"`), io.Discard)
}
func (d Database) Configure(ctx context.Context, password string) error {
	if err := dbexec.ValidatePassword(password); err != nil {
		return err
	}
	// Passwords are expanded inside the container, never in Docker request arguments.
	var users []struct {
		User string `json:"user"`
	}
	if err := d.query(ctx, "list_users", &users); err != nil {
		return err
	}
	exists := false
	for _, u := range users {
		if u.User == "app" {
			exists = true
		}
	}
	verb := "add_user"
	if exists {
		verb = "change_password"
	}
	permission := ".*"
	if d.Recovery {
		permission = "^$"
	}
	return d.Run(ctx, command(`ctl "$1" app "$LOOMPLANE_APP_PASSWORD"
ctl set_user_tags app
ctl set_permissions -p / app "$2" "$2" "$2"
ctl authenticate_user app "$LOOMPLANE_APP_PASSWORD"`, verb, permission), io.Discard)
}
func (d Database) query(ctx context.Context, verb string, out any) error {
	var b bytes.Buffer
	if err := d.Run(ctx, command(`ctl "$@" --formatter json`, verb), &limitedWriter{Writer: &b, remaining: 1 << 20}); err != nil {
		return err
	}
	return json.Unmarshal(b.Bytes(), out)
}
func (d Database) queues(ctx context.Context) ([]queue, error) {
	var b bytes.Buffer
	err := d.Run(ctx, command(`ctl list_queues name durable auto_delete type messages messages_persistent arguments --formatter json`), &limitedWriter{Writer: &b, remaining: 1 << 20})
	if err != nil {
		return nil, err
	}
	var q []queue
	if err := json.Unmarshal(b.Bytes(), &q); err != nil {
		return nil, err
	}
	normalizePersistence(q)
	slices.SortFunc(q, func(a, b queue) int {
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	})
	return q, nil
}

type limitedWriter struct {
	io.Writer
	remaining int64
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining {
		return 0, errors.New("RabbitMQ backup exceeds its size limit")
	}
	n, err := w.Writer.Write(p)
	w.remaining -= int64(n)
	return n, err
}

func (d Database) Export(ctx context.Context, files *backupfiles.Store, key string) (backupfiles.Object, error) {
	f, err := os.CreateTemp("", "loomplane-rabbitmq-backup-")
	if err != nil {
		return backupfiles.Object{}, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if runErr := d.Run(ctx, command(backupScript), &limitedWriter{Writer: f, remaining: maxArchive}); runErr != nil {
		return backupfiles.Object{}, runErr
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return backupfiles.Object{}, err
	}
	if _, err = readArchive(f); err != nil {
		return backupfiles.Object{}, err
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return backupfiles.Object{}, err
	}
	return files.Put(ctx, key, f)
}

func (d Database) Restore(ctx context.Context, files *backupfiles.Store, object backupfiles.Object) (err error) {
	if !d.Recovery || d.Upload == nil || d.Run == nil {
		return errors.New("restore requires an isolated RabbitMQ copy")
	}
	if object.Size <= 0 || object.Size > maxArchive {
		return errors.New("invalid RabbitMQ archive size")
	}
	f, err := os.CreateTemp("", "loomplane-rabbitmq-restore-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if runErr := files.CopyVerified(ctx, object, &limitedWriter{Writer: f, remaining: maxArchive}); runErr != nil {
		return runErr
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	manifest, err := readArchive(f)
	if err != nil {
		return err
	}
	existing, err := d.queues(ctx)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return errors.New("RabbitMQ recovery destination must have no queues")
	}
	name := "loomplane-rabbitmq-" + rand.Text() + ".tar"
	defer func() { err = errors.Join(err, dbexec.Cleanup(ctx, d.Run, name)) }()
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if runErr := d.Upload(ctx, name, object.Size, func(w io.Writer) error { _, e := io.Copy(w, f); return e }); runErr != nil {
		return runErr
	}
	if runErr := d.Run(ctx, command(restoreScript, "/tmp/"+name), io.Discard); runErr != nil {
		return runErr
	}
	actual, err := d.queues(ctx)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(manifest.Queues, actual) {
		return errors.New("RabbitMQ restored queue state differs from the backup; expired messages cannot be verified as recovered")
	}
	return d.Probe(ctx)
}

// Validate while clients are still connected, then again after quiescing.
// Closing an exclusive queue's connection or stopping transient messages would
// itself discard unsupported data, so archive validation alone is too late.
const validateProfile = `
Read = fun(Name) ->
 {ok, B} = file:read_file(filename:join("@DIRECTORY@", Name)),
 rabbit_json:decode(B)
end,
Qs = Read("queues.json"),
true = is_list(Qs) andalso length(Qs) =< 100,
true = lists:all(fun(Q) ->
 Type = maps:get(<<"type">>, Q),
 maps:get(<<"durable">>, Q) andalso not maps:get(<<"auto_delete">>, Q)
 andalso lists:member(Type, [<<"classic">>, <<"quorum">>])
 andalso (Type =:= <<"quorum">> orelse maps:get(<<"messages">>, Q) =:= maps:get(<<"messages_persistent">>, Q))
 andalso lists:all(fun(A) -> A =:= [<<"x-queue-type">>, <<"longstr">>, Type] end, maps:get(<<"arguments">>, Q))
end, Qs),
[#{<<"name">> := <<"/">>}] = Read("vhosts.json"),
[<<"admin">>, <<"app">>] = lists:sort([maps:get(<<"user">>, U) || U <- Read("users.json")]),
[] = Read("policies.json"),
[] = Read("parameters.json"),
#{<<"disk_nodes">> := [<<"rabbit@localhost">>], <<"running_nodes">> := [<<"rabbit@localhost">>]} = Read("cluster.json"),
ok.
`

const backupScript = `
exec 9>/tmp/loomplane-rabbitmq-backup.lock
flock -n 9
for stale in /tmp/loomplane-rabbitmq.??????; do
 if [ -d "$stale" ]; then rm -rf "$stale"; fi
done
work=
stopped=0
suspended=0
finish() {
 status=$?
 trap - EXIT HUP INT TERM
 if [ "$stopped" = 1 ]; then ctl start_app >&2 || status=1; fi
 if [ "$suspended" = 1 ]; then ctl resume_listeners >&2 || status=1; fi
 if [ -n "$work" ]; then rm -rf "$work"; fi
 exit "$status"
}
trap finish EXIT
trap 'exit 1' HUP INT TERM
work=$(mktemp -d /tmp/loomplane-rabbitmq.XXXXXX)
inventory() {
ctl list_queues name durable auto_delete type messages messages_persistent arguments --formatter json > "$work/queues.json"
ctl list_vhosts name --formatter json > "$work/vhosts.json"
ctl list_users --formatter json > "$work/users.json"
ctl list_policies --formatter json > "$work/policies.json"
ctl list_parameters --formatter json > "$work/parameters.json"
ctl cluster_status --formatter json > "$work/cluster.json"
for metadata in "$work"/*.json; do test "$(wc -c < "$metadata")" -le 1048576; done
chown rabbitmq:rabbitmq "$work" "$work"/*.json
ctl eval "$(printf '%s' '` + validateProfile + `' | sed "s|@DIRECTORY@|$work|g")" >&2
}
inventory
suspended=1
ctl suspend_listeners >&2
ctl close_all_connections --global --per-connection-delay 0 'Loomplane message backup' >&2
inventory
printf '%s' '{"format":"rabbitmq-node-v1","node":"rabbit@localhost","version":"4.3.6"}' > "$work/profile.json"
stopped=1
ctl stop_app >&2
test "$(du -sb /var/lib/rabbitmq/mnesia | cut -f1)" -le 1073741824
cp -a /var/lib/rabbitmq/mnesia "$work/mnesia"
tar -cf "$work/backup.tar" -C "$work" profile.json queues.json vhosts.json users.json policies.json parameters.json cluster.json mnesia
ctl start_app >&2
stopped=0
ctl resume_listeners >&2
suspended=0
cat "$work/backup.tar"
`

const restoreScript = `
exec 9>/tmp/loomplane-rabbitmq-restore.lock
flock -n 9
ctl stop_app
rm -rf /var/lib/rabbitmq/mnesia
mkdir -p /var/lib/rabbitmq/mnesia
tar -xf "$1" -C /var/lib/rabbitmq mnesia
chown -R rabbitmq:rabbitmq /var/lib/rabbitmq/mnesia
ctl start_app
ctl change_password admin "$RABBITMQ_DEFAULT_PASS"
ctl change_password app "$LOOMPLANE_APP_PASSWORD"
ctl set_user_tags app
ctl set_permissions -p / app '^$' '^$' '^$'
ctl authenticate_user app "$LOOMPLANE_APP_PASSWORD"
`
