//go:build integration

package integration

import (
	"context"

	"github.com/xraph/loomplane/internal/security"
)

type nativeAccessAllow struct{}

func (nativeAccessAllow) Require(_ context.Context, p security.Principal, permission security.Permission, project string) error {
	if p.OrgID != "org" || p.UserID != "operator" || project != "project" || permission != security.BackupRead && permission != security.RestoreManage && permission != security.SecretManage {
		return security.ErrForbidden
	}
	return nil
}
