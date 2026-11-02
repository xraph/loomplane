package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/xraph/ctrlplane/id"

	"github.com/xraph/loomplane/internal/clouds/dbexec"
	"github.com/xraph/loomplane/internal/clouds/owned"
	native "github.com/xraph/loomplane/internal/clouds/rabbitmq"
)

func nativeRabbitMQ(c *Client, container string, labels map[string]string) native.Database {
	return native.Database{Recovery: labels[LabelRecovery] != "", Run: func(ctx context.Context, args []string, out io.Writer) error {
		return ExecOutput(ctx, c, container, args, out)
	}, Upload: func(ctx context.Context, name string, size int64, write func(io.Writer) error) error {
		return putContainerFile(ctx, c, container, name, size, write)
	}}
}
func (r *Runtime) withRabbitMQ(ctx context.Context, iid id.ID, labels map[string]string, password string, run func(native.Database) error) error {
	c, err := r.Session(ctx, labels)
	if err != nil {
		return err
	}
	defer c.Close()
	info, err := r.inspect(ctx, c, ContainerName(iid), iid, labels)
	if err != nil {
		return err
	}
	if !info.State.Running || labels[native.LabelEngine] != "rabbitmq" || labels[owned.LabelImage] == "" || info.Config.Image != labels[owned.LabelImage] || info.Config.Labels[native.LabelEngine] != "rabbitmq" || info.Config.Labels[owned.LabelConnection] != labels[owned.LabelConnection] || info.Config.Labels[LabelRecovery] != labels[LabelRecovery] {
		return ErrOwned
	}
	appPassword := ""
	for _, entry := range info.Config.Env {
		if v, ok := strings.CutPrefix(entry, "LOOMPLANE_APP_PASSWORD="); ok {
			appPassword = v
		}
	}
	if err := dbexec.ValidatePassword(appPassword); err != nil {
		return err
	}
	if password != "" && password != appPassword {
		return errors.New("RabbitMQ application credential differs")
	}
	if labels[LabelRecovery] != "" {
		if labels[owned.LabelPublic] != "false" || info.Config.Labels[owned.LabelPublic] != "false" || len(info.NetworkSettings.Networks) != 1 {
			return ErrOwned
		}
		for _, ports := range info.NetworkSettings.Ports {
			if len(ports) > 0 {
				return errors.New("RabbitMQ recovery has a published port")
			}
		}
		network, ok := info.NetworkSettings.Networks[runtimeNetwork(labels)]
		if !ok || network.NetworkID == "" {
			return ErrOwned
		}
		var actual struct {
			Internal bool
			Labels   map[string]string
		}
		if err := dockerJSON(ctx, c, http.MethodGet, "/networks/"+url.PathEscape(network.NetworkID), nil, &actual); err != nil {
			return err
		}
		if !actual.Internal || actual.Labels[owned.LabelOrg] != labels[owned.LabelOrg] || actual.Labels[owned.LabelEnvironment] != labels[owned.LabelEnvironment] {
			return errors.New("RabbitMQ recovery network is not isolated")
		}
	}
	return run(nativeRabbitMQ(c, ContainerName(iid), labels))
}
