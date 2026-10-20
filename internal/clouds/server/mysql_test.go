package server

import (
	"context"
	"testing"

	"github.com/xraph/ctrlplane/id"

	nativemysql "github.com/xraph/loomplane/internal/clouds/mysql"
)

func TestInvalidMySQLPasswordDoesNotOpenTarget(t *testing.T) {
	r := Runtime{Session: func(context.Context, map[string]string) (*Client, error) {
		t.Fatal("resolved target credentials for invalid input")
		return nil, nil
	}}
	if err := r.ConfigureMySQL(t.Context(), id.New(id.PrefixInstance), map[string]string{nativemysql.LabelDatabase: "database", nativemysql.LabelEngine: "mysql"}, "short"); err == nil {
		t.Fatal("invalid password accepted")
	}
}
