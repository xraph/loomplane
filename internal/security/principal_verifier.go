package security

import "context"

// PrincipalVerifier rechecks the credential behind a saved principal.
// User permissions are checked separately for the requested operation.
type PrincipalVerifier interface {
	ValidatePrincipal(context.Context, Principal) error
}
