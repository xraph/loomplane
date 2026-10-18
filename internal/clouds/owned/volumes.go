package owned

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/xraph/ctrlplane/id"
	cpprovider "github.com/xraph/ctrlplane/provider"
)

const LabelVolumeRole = "loomplane.volume.role"
const LabelStateful = "loomplane.stateful"

var volumeRole = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// PersistentVolumeName outlives deployment revisions and separates fresh restores.
func PersistentVolumeName(iid id.ID, role string) string {
	sum := sha256.Sum256([]byte(iid.String() + "/" + role))
	return fmt.Sprintf("lpv-%x", sum[:20])
}

// ValidatePersistentVolumes validates shared mount safety. Capacity and storage
// backend support remain the responsibility of each target adapter.
func ValidatePersistentVolumes(volumes []cpprovider.VolumeSpec) error {
	if len(volumes) > 16 {
		return errors.New("too many persistent volumes")
	}
	names, mounts := map[string]bool{}, map[string]bool{}
	for _, v := range volumes {
		if v.Type != "persistent" || !volumeRole.MatchString(v.Name) || v.MountPath == "/" || !strings.HasPrefix(v.MountPath, "/") || path.Clean(v.MountPath) != v.MountPath || strings.ContainsAny(v.MountPath, "\\\x00") {
			return errors.New("volume needs a persistent role and a clean absolute mount path")
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
