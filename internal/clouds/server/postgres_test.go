package server

import (
	"context"
	"testing"

	"github.com/xraph/ctrlplane/id"
)

func TestInvalidPostgresPasswordDoesNotOpenTarget(t *testing.T) {
	r := Runtime{Session: func(context.Context, map[string]string) (*Client, error) {
		t.Fatal("resolved credentials for an invalid password")
		return nil, nil
	}}
	if err := r.ConfigurePostgres(t.Context(), id.New(id.PrefixInstance), map[string]string{LabelDatabase: "database"}, "short"); err == nil {
		t.Fatal("invalid password accepted")
	}
}
