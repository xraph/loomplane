package cloudinfra

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/extensions/identity"
	infra "github.com/xraph/loomplane/internal/cloudinfra"
)

type ManagedRegistryBackup struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	Size      int64     `json:"size"`
}
type ManagedRegistryView struct {
	ConnectionID string                  `json:"connection_id"`
	Generation   int64                   `json:"generation"`
	Hostname     string                  `json:"hostname"`
	Status       string                  `json:"status"`
	Message      string                  `json:"message"`
	Action       string                  `json:"action"`
	Phase        string                  `json:"phase"`
	DNSReady     bool                    `json:"dns_ready"`
	Backups      []ManagedRegistryBackup `json:"backups"`
}

func publicRegistry(state *infra.RegistryState) *ManagedRegistryView {
	if state == nil {
		return nil
	}
	out := &ManagedRegistryView{ConnectionID: state.ConnectionID, Generation: state.Generation, Hostname: state.Hostname, Status: state.Status, Message: state.Message, Backups: []ManagedRegistryBackup{}}
	if state.Action != nil {
		out.Action = state.Action.Kind
		out.Phase = state.Action.Phase
	}
	out.DNSReady = state.DNS != nil && state.DNS.Receipt != nil && !state.DNS.Attempted
	for _, backup := range state.Backups {
		out.Backups = append(out.Backups, ManagedRegistryBackup{ID: backup.ID, CreatedAt: backup.CreatedAt, Size: backup.Object.Size})
	}
	return out
}

func (e *Extension) registryRoutes(r forge.Router, guard identity.Guard) error {
	group := r.Group("/api/v1/connections/:connectionId/registry", forge.WithGroupMiddleware(guard...), forge.WithGroupTags("Managed registries"))
	if err := group.GET("", e.registry, forge.WithName("registries.get"), forge.WithSummary("Read registry DNS, maintenance and backups"), forge.WithResponseSchema(http.StatusOK, "Registry", ManagedRegistryView{})); err != nil {
		return err
	}
	return group.POST("/actions", e.registryAction, forge.WithName("registries.action"), forge.WithSummary("Queue registry maintenance"), forge.WithRequestSchema(infra.RegistryActionInput{}), forge.WithResponseSchema(http.StatusAccepted, "Registry", ManagedRegistryView{}), forge.WithInvalidates("ManagedRegistryView"))
}
func (e *Extension) registry(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	state, err := e.svc.GetRegistry(c.Request().Context(), p, c.Param("connectionId"))
	return result(c, http.StatusOK, publicRegistry(state), err)
}
func (e *Extension) registryAction(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	var input infra.RegistryActionInput
	decoder := json.NewDecoder(http.MaxBytesReader(c.Response(), c.Request().Body, 16<<10))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&input); err != nil {
		return result(c, 0, nil, infra.ErrInvalid)
	}
	if err = decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return result(c, 0, nil, infra.ErrInvalid)
	}
	state, err := e.svc.RequestRegistryAction(c.Request().Context(), p, c.Param("connectionId"), input)
	return result(c, http.StatusAccepted, publicRegistry(state), err)
}
