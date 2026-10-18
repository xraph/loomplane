package connections

import (
	"net/http"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/clouds/owned"
)

// StorageVolumeList is one connection's visible persistent storage inventory.
type StorageVolumeList struct {
	Volumes []owned.StoredVolume `json:"volumes"`
}

func (StorageVolumeList) ForgeEnvelope() forge.EnvelopeDef {
	return forge.EnvelopeDef{ItemsField: "volumes"}
}

func (e *Extension) storage(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	volumes, err := e.svc.StorageVolumes(c.Request().Context(), p, c.Param("id"))
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, StorageVolumeList{Volumes: volumes})
}
