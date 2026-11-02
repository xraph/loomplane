package nativepg

import (
	"context"
	"strings"
	"time"
)

// Probe authenticates an existing source login without creating a recovery role.
// The provider transport owns TLS; the query verifies database, user and version.
func (v *Verifier) Probe(ctx context.Context, spec Spec, check func(context.Context) error) (Proof, error) {
	if v == nil || v.Dial == nil || check == nil || spec.Database == "" || len(spec.Database) > 63 || spec.AdminUser == "" || spec.AdminPassword == "" || spec.MajorVersion != 17 || strings.ContainsRune(spec.Database+spec.AdminUser+spec.AdminPassword, 0) {
		return Proof{}, ErrVerification
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := check(ctx); err != nil {
		return Proof{}, err
	}
	conn, err := v.connect(ctx, spec, spec.AdminUser, spec.AdminPassword, true)
	if err != nil {
		return Proof{}, sqlError("authenticate the source login", err)
	}
	defer closeSQL(conn)
	proof, err := observe(ctx, conn, spec.Database, spec.AdminUser, spec.MajorVersion)
	if err != nil {
		return Proof{}, err
	}
	if !proof.TransactionReadOnly {
		return Proof{}, ErrVerification
	}
	if guardErr := check(ctx); guardErr != nil {
		return Proof{}, guardErr
	}
	return proof, nil
}
