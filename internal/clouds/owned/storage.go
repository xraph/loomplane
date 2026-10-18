package owned

import (
	"github.com/xraph/ctrlplane/id"

	"github.com/xraph/loomplane/pkg/provider"
)

// StoredVolume is the shared public inventory contract.
type StoredVolume = provider.StoredVolume

// InventoryVolume rejects incomplete ownership and names that do not match the
// instance/role identity. Provider-side label filters are not authorization.
func InventoryVolume(name, org, connection string, labels map[string]string) (StoredVolume, bool) {
	iid, err := id.Parse(labels[LabelInstance])
	role := labels[LabelVolumeRole]
	if err != nil || !volumeRole.MatchString(role) || org == "" || connection == "" || labels[LabelOrg] != org || labels[LabelConnection] != connection || labels[LabelProject] == "" || labels[LabelEnvironment] == "" || name != PersistentVolumeName(iid, role) {
		return StoredVolume{}, false
	}
	return StoredVolume{ID: connection + ":" + name, Name: name, ConnectionID: connection, ProjectID: labels[LabelProject], EnvironmentID: labels[LabelEnvironment], InstanceID: iid.String(), Role: role, Status: "retained"}, true
}
