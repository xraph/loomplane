package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"

	"github.com/xraph/loomplane/internal/clouds/owned"
)

// StorageVolumes reads persistent volume metadata and current attachments.
func (c *Client) StorageVolumes(ctx context.Context, org, connection string) ([]owned.StoredVolume, error) {
	var daemon struct{ ID string }
	if err := dockerJSON(ctx, c, http.MethodGet, "/info", nil, &daemon); err != nil {
		return nil, err
	}
	if c.Config == nil || c.Config.Details["docker_id"] == "" || daemon.ID != c.Config.Details["docker_id"] {
		return nil, errors.New("the checked Docker daemon identity changed")
	}
	filter, err := json.Marshal(map[string][]string{"label": {owned.LabelOrg + "=" + org, owned.LabelConnection + "=" + connection}})
	if err != nil {
		return nil, err
	}
	var inventory struct {
		Volumes []struct {
			Name, Driver string
			Labels       map[string]string
		}
	}
	if readErr := dockerJSON(ctx, c, http.MethodGet, "/volumes?filters="+url.QueryEscape(string(filter)), nil, &inventory); readErr != nil {
		return nil, readErr
	}
	var containers []struct{ Mounts []struct{ Type, Name string } }
	if readErr := dockerJSON(ctx, c, http.MethodGet, "/containers/json?all=true", nil, &containers); readErr != nil {
		return nil, readErr
	}
	attached := map[string]bool{}
	for _, container := range containers {
		for _, mount := range container.Mounts {
			if mount.Type == "volume" {
				attached[mount.Name] = true
			}
		}
	}
	volumes := make([]owned.StoredVolume, 0, len(inventory.Volumes))
	for _, native := range inventory.Volumes {
		volume, ok := owned.InventoryVolume(native.Name, org, connection, native.Labels)
		if !ok || native.Driver != "local" {
			continue
		}
		volume.Provider = "server"
		volume.StorageClass = native.Driver
		if attached[native.Name] {
			volume.Status = "in-use"
		}
		volumes = append(volumes, volume)
	}
	return volumes, nil
}
