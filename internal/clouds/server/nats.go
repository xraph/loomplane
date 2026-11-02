package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	client "github.com/nats-io/nats.go"
	"github.com/xraph/ctrlplane/id"

	"github.com/xraph/loomplane/internal/backupfiles"
	"github.com/xraph/loomplane/internal/clouds/dbexec"
	native "github.com/xraph/loomplane/internal/clouds/nats"
	"github.com/xraph/loomplane/internal/clouds/owned"
)

type natsDialer struct{ dial func() (net.Conn, error) }

func (d natsDialer) Dial(_, _ string) (net.Conn, error) { return d.dial() }

func (r *Runtime) natsDatabase(c *Client, iid id.ID, labels map[string]string, expectedPassword string) native.Database {
	return native.Database{Recovery: labels[LabelRecovery] != "", Connect: func(ctx context.Context, app bool) (*client.Conn, error) {
		info, err := r.inspect(ctx, c, ContainerName(iid), iid, labels)
		if err != nil {
			return nil, err
		}
		if !info.State.Running || labels[native.LabelEngine] != "nats" || labels[owned.LabelImage] == "" || info.Config.Image != labels[owned.LabelImage] || info.Config.Labels[native.LabelEngine] != "nats" || info.Config.Labels[owned.LabelConnection] != labels[owned.LabelConnection] || info.Config.Labels[LabelRecovery] != labels[LabelRecovery] {
			return nil, ErrOwned
		}
		network, ok := info.NetworkSettings.Networks[runtimeNetwork(labels)]
		if !ok || net.ParseIP(network.IPAddress) == nil || network.NetworkID == "" {
			return nil, ErrOwned
		}
		if labels[LabelRecovery] != "" {
			if labels[owned.LabelPublic] != "false" || info.Config.Labels[owned.LabelPublic] != "false" || len(info.NetworkSettings.Networks) != 1 {
				return nil, ErrOwned
			}
			for _, ports := range info.NetworkSettings.Ports {
				if len(ports) > 0 {
					return nil, errors.New("JetStream recovery has a published port")
				}
			}
			var actual struct {
				Internal bool
				Labels   map[string]string
			}
			if checkErr := dockerJSON(ctx, c, http.MethodGet, "/networks/"+url.PathEscape(network.NetworkID), nil, &actual); checkErr != nil {
				return nil, checkErr
			}
			if !actual.Internal || actual.Labels[owned.LabelOrg] != labels[owned.LabelOrg] || actual.Labels[owned.LabelEnvironment] != labels[owned.LabelEnvironment] {
				return nil, errors.New("JetStream recovery network is not isolated")
			}
		}
		user, key := "admin", "LOOMPLANE_NATS_ADMIN_PASSWORD="
		if app {
			user, key = "app", "LOOMPLANE_APP_PASSWORD="
		}
		password := ""
		for _, entry := range info.Config.Env {
			if value, found := strings.CutPrefix(entry, key); found {
				password = value
			}
		}
		if checkErr := dbexec.ValidatePassword(password); checkErr != nil {
			return nil, checkErr
		}
		if app && expectedPassword != "" && password != expectedPassword {
			return nil, errors.New("JetStream application credential differs")
		}
		var raw net.Conn
		var stopHandshake func() bool
		connectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		nc, err := client.Connect("nats://127.0.0.1:4222", client.UserInfo(user, password), client.NoReconnect(), client.Timeout(10*time.Second), client.SetCustomDialer(natsDialer{dial: func() (net.Conn, error) {
			var dialErr error
			raw, dialErr = c.DialContext(connectCtx, "tcp", net.JoinHostPort(network.IPAddress, "4222"))
			if dialErr == nil {
				stopHandshake = context.AfterFunc(connectCtx, func() { _ = raw.Close() })
			}
			return raw, dialErr
		}}), client.ErrorHandler(func(*client.Conn, *client.Subscription, error) {}))
		if stopHandshake != nil {
			stopHandshake()
		}
		if err != nil {
			return nil, errors.New("JetStream native authentication or connection failed")
		}
		stop := context.AfterFunc(ctx, func() { nc.Close() })
		nc.SetClosedHandler(func(*client.Conn) { stop() })
		return nc, nil
	}}
}
func (r *Runtime) withNATS(ctx context.Context, iid id.ID, labels map[string]string, run func(native.Database) error) error {
	c, err := r.Session(ctx, labels)
	if err != nil {
		return err
	}
	defer c.Close()
	return run(r.natsDatabase(c, iid, labels, ""))
}
func (r *Runtime) backupNATS(ctx context.Context, iid id.ID, labels map[string]string, files *backupfiles.Store, key string) (object backupfiles.Object, err error) {
	err = r.withNATS(ctx, iid, labels, func(db native.Database) error { var e error; object, e = db.Export(ctx, files, key); return e })
	return object, err
}
