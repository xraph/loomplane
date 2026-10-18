package server

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"path"
	"regexp"
	"strings"

	"github.com/xraph/ctrlplane/id"
	cpprovider "github.com/xraph/ctrlplane/provider"

	"github.com/xraph/loomplane/internal/clouds/owned"
)

const labelVolumeRole = "loomplane.volume.role"

var volumeRole = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

type volumeMount struct {
	Type          string
	Source        string
	Target        string
	VolumeOptions struct{ NoCopy bool }
}

// VolumeName stays the same across runtime revisions. A different instance
// gets different storage, including when restoring into a fresh instance.
func VolumeName(iid id.ID, role string) string {
	sum := sha256.Sum256([]byte(iid.String() + "/" + role))
	return fmt.Sprintf("lpv-%x", sum[:20])
}

func validateVolumes(volumes []cpprovider.VolumeSpec) error {
	if len(volumes) > 16 {
		return errors.New("too many persistent volumes")
	}
	names, mounts := map[string]bool{}, map[string]bool{}
	for _, v := range volumes {
		if v.Type != "persistent" || !volumeRole.MatchString(v.Name) || v.MountPath == "/" || !strings.HasPrefix(v.MountPath, "/") || path.Clean(v.MountPath) != v.MountPath || strings.ContainsAny(v.MountPath, "\\\x00") {
			return errors.New("volume needs a persistent role and a clean absolute mount path")
		}
		// Docker's local driver shares host capacity; it cannot promise a disk quota.
		if v.SizeMB != 0 {
			return errors.New("docker local volumes use shared host capacity; size quotas are unsupported")
		}
		if names[v.Name] || mounts[v.MountPath] {
			return errors.New("duplicate volume role or mount")
		}
		for mount := range mounts {
			if strings.HasPrefix(mount, v.MountPath+"/") || strings.HasPrefix(v.MountPath, mount+"/") {
				return errors.New("overlapping volume mounts")
			}
		}
		names[v.Name], mounts[v.MountPath] = true, true
	}
	return nil
}

func ensureVolumes(ctx context.Context, c *Client, iid id.ID, volumes []cpprovider.VolumeSpec, labels map[string]string) ([]volumeMount, error) {
	mounts := make([]volumeMount, 0, len(volumes))
	for _, v := range volumes {
		expected := map[string]string{owned.LabelInstance: iid.String(), labelVolumeRole: v.Name}
		for _, key := range []string{owned.LabelOrg, owned.LabelProject, owned.LabelEnvironment, owned.LabelConnection} {
			if labels[key] == "" {
				return nil, errors.New("persistent volume needs complete ownership")
			}
			expected[key] = labels[key]
		}
		name := VolumeName(iid, v.Name)
		var volume struct {
			Name, Driver    string
			Labels, Options map[string]string
		}
		err := dockerJSON(ctx, c, http.MethodGet, "/volumes/"+name, nil, &volume)
		if errors.Is(err, ErrMissing) {
			if createErr := dockerJSON(ctx, c, http.MethodPost, "/volumes/create", map[string]any{"Name": name, "Driver": "local", "Labels": expected}, nil); createErr != nil {
				return nil, createErr
			}
			// Docker create is idempotent. Read back to detect a concurrent collision.
			err = dockerJSON(ctx, c, http.MethodGet, "/volumes/"+name, nil, &volume)
		}
		if err != nil {
			return nil, err
		}
		if volume.Name != name || volume.Driver != "local" || len(volume.Options) > 0 {
			return nil, ErrOwned
		}
		for key, value := range expected {
			if volume.Labels[key] != value {
				return nil, ErrOwned
			}
		}
		m := volumeMount{Type: "volume", Source: name, Target: v.MountPath}
		m.VolumeOptions.NoCopy = true
		mounts = append(mounts, m)
	}
	return mounts, nil
}

// A rollout cannot silently switch data directories or adopt an image volume.
func checkVolumeLayout(existing *containerInfo, iid id.ID, volumes []cpprovider.VolumeSpec) error {
	expected := map[string]string{}
	for _, v := range volumes {
		expected[v.MountPath] = VolumeName(iid, v.Name)
	}
	for _, m := range existing.Mounts {
		if m.Type == "volume" && strings.HasPrefix(m.Name, "lpv-") {
			if expected[m.Destination] != m.Name {
				return errors.New("persistent volume layout changed; migrate into a fresh service")
			}
		}
		if name, ok := expected[m.Destination]; ok {
			if m.Type != "volume" || m.Name != name {
				return ErrOwned
			}
			delete(expected, m.Destination)
		}
	}
	if len(expected) > 0 {
		return errors.New("persistent volume layout changed; migrate into a fresh service")
	}
	return nil
}
