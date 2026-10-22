// Package cloudinfra binds account infrastructure APIs independently of runtime
// targets. Services persist their own organization and resource ownership state.
package cloudinfra

import (
	"fmt"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/pkg/provider"
)

// Credential resolves the already-authorized connection's immutable scope. The
// caller must obtain raw from Vault only after organization/grant authorization.
// Connection readiness for App Platform says nothing about Droplet/DNS scopes;
// adapters verify identity and the required read access on each operation.
func Credential(connection *connections.Connection, raw []byte) (provider.InfrastructureCredential, error) {
	if connection == nil {
		return provider.InfrastructureCredential{}, provider.ErrInfrastructureInvalid
	}
	credential := provider.InfrastructureCredential{Provider: connection.Cloud, Token: string(raw)}
	switch connection.Cloud {
	case connections.DigitalOcean:
		credential.AccountID = connection.Details["account_uuid"]
		if team := connection.Details["team_uuid"]; team != "" {
			credential.TeamID = team
			credential.AccountID = team
		}
	case connections.Vercel:
		credential.AccountID = connection.Details["user_id"]
		if team := connection.Details["team_id"]; team != "" {
			credential.TeamID = team
			credential.AccountID = team
		}
	default:
		return provider.InfrastructureCredential{}, &provider.UnsupportedCapabilityError{Provider: connection.Cloud, Capability: "infrastructure"}
	}
	if credential.AccountID == "" || credential.Token == "" {
		return provider.InfrastructureCredential{}, fmt.Errorf("%w: verify the connection's immutable account first", provider.ErrInfrastructureIdentity)
	}
	return credential, nil
}
