package octopusloom

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/catalogproxy"
	"github.com/xraph/loomplane/internal/recipes"
)

func TestOctopusProxyTarget(t *testing.T) {
	got := AdminTarget()
	if got.Component != "gateway" || got.Port != "admin" || got.Root != "/admin" || got.StripPrefix != "" ||
		!slices.Equal(got.AllowedPrefixes, []string{"/admin"}) {
		t.Fatalf("target = %+v", got)
	}
	if got.Header == nil {
		t.Fatal("the target sends no admin token")
	}
	var asked []string
	header, err := got.Header(context.Background(), func(name string) ([]byte, error) {
		asked = append(asked, name)
		return []byte("tok"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(asked, []string{"admin-api-token"}) {
		t.Fatalf("read secrets %v", asked)
	}
	if len(header) != 1 || header.Get("X-Octopus-Admin-Token") != "tok" {
		t.Fatalf("header = %v, want only X-Octopus-Admin-Token: tok", header)
	}

	// A secret that can't be read is an error that doesn't repeat it.
	_, err = got.Header(context.Background(), func(string) ([]byte, error) { return nil, errors.New("vault down") })
	if err == nil {
		t.Fatal("no error without the token")
	}

	// init() registered it: registering again is refused as a duplicate.
	defer func() {
		r := recover()
		if r == nil || !strings.Contains(fmt.Sprint(r), "registered twice") {
			t.Fatalf("registering octopus again: %v, want a duplicate panic", r)
		}
	}()
	catalogproxy.Register(recipes.OctopusKind, AdminTarget())
}
