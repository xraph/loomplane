package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// PurgeReport is what Purge removed from the Docker daemon, by name, and the
// Loomplane volumes it left because volumes were not asked for.
type PurgeReport struct {
	Containers  []string `json:"containers"`
	Networks    []string `json:"networks"`
	Volumes     []string `json:"volumes"`
	KeptVolumes []string `json:"kept_volumes,omitempty"`
}

// labelFilter asks the daemon for objects that carry the Loomplane label,
// whatever its value.
var labelFilter = url.QueryEscape(`{"label":["` + loomplaneLabel + `"]}`)

// Purge removes what Loomplane put on the Docker daemon at socket: every
// container carrying the label loomplane.org, forced (stopped and removed,
// its anonymous volumes kept), then every such network, then, only when
// volumes is set, every such volume. The daemon is asked for labelled
// objects only, and each one's labels are checked again here, so nothing
// else on the machine is touched. An object the daemon refuses to remove
// does not stop the rest; the error names each one.
func Purge(ctx context.Context, socket string, volumes bool) (PurgeReport, error) {
	api := dockerAPI(socket)
	rep := PurgeReport{Containers: []string{}, Networks: []string{}, Volumes: []string{}}
	var failed []error

	var containers []struct {
		ID     string            `json:"Id"`
		Names  []string          `json:"Names"`
		Labels map[string]string `json:"Labels"`
	}
	if err := dockerGetJSON(ctx, api, "/containers/json?all=1&filters="+labelFilter, &containers); err != nil {
		return rep, fmt.Errorf("listing the Loomplane containers: %w", err)
	}
	for _, c := range containers {
		if !hasLoomplaneLabel(c.Labels) {
			continue
		}
		name := c.ID
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		// force stops it first. v is left off, so a container's anonymous
		// volumes stay with the data in them.
		if err := dockerDelete(ctx, api, "/containers/"+url.PathEscape(c.ID)+"?force=1"); err != nil {
			failed = append(failed, fmt.Errorf("container %s: %w", name, err))
			continue
		}
		rep.Containers = append(rep.Containers, name)
	}

	var networks []struct {
		ID     string            `json:"Id"`
		Name   string            `json:"Name"`
		Labels map[string]string `json:"Labels"`
	}
	if err := dockerGetJSON(ctx, api, "/networks?filters="+labelFilter, &networks); err != nil {
		return rep, errors.Join(append(failed, fmt.Errorf("listing the Loomplane networks: %w", err))...)
	}
	for _, n := range networks {
		if !hasLoomplaneLabel(n.Labels) {
			continue
		}
		if err := dockerDelete(ctx, api, "/networks/"+url.PathEscape(n.ID)); err != nil {
			failed = append(failed, fmt.Errorf("network %s: %w", n.Name, err))
			continue
		}
		rep.Networks = append(rep.Networks, n.Name)
	}

	var vols struct {
		Volumes []struct {
			Name   string            `json:"Name"`
			Labels map[string]string `json:"Labels"`
		} `json:"Volumes"`
	}
	if err := dockerGetJSON(ctx, api, "/volumes?filters="+labelFilter, &vols); err != nil {
		return rep, errors.Join(append(failed, fmt.Errorf("listing the Loomplane volumes: %w", err))...)
	}
	for _, v := range vols.Volumes {
		if !hasLoomplaneLabel(v.Labels) {
			continue
		}
		if !volumes {
			rep.KeptVolumes = append(rep.KeptVolumes, v.Name)
			continue
		}
		if err := dockerDelete(ctx, api, "/volumes/"+url.PathEscape(v.Name)); err != nil {
			failed = append(failed, fmt.Errorf("volume %s: %w", v.Name, err))
			continue
		}
		rep.Volumes = append(rep.Volumes, v.Name)
	}
	return rep, errors.Join(failed...)
}

func hasLoomplaneLabel(labels map[string]string) bool {
	_, ok := labels[loomplaneLabel]
	return ok
}

// dockerDelete sends DELETE path to the daemon. Something already gone
// counts as removed.
func dockerDelete(ctx context.Context, api *http.Client, path string) error {
	// The host is a placeholder: api dials only the daemon socket.
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, "http://docker"+path, http.NoBody)
	if err != nil {
		return err
	}
	resp, err := api.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 == 2 || resp.StatusCode == http.StatusNotFound {
		return nil
	}
	var msg struct {
		Message string `json:"message"`
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10)) //nolint:errcheck // the status says enough without it
	if json.Unmarshal(body, &msg) == nil && msg.Message != "" {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg.Message)
	}
	return fmt.Errorf("HTTP %d", resp.StatusCode)
}
