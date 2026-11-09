package discovery

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	cloudconn "github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/repositories"
)

// revealing is a Vault that answers every Reveal with err.
type revealing struct{ err error }

func (revealing) Put(context.Context, string, string, []byte) error { return nil }
func (r revealing) Reveal(context.Context, string, string) ([]byte, error) {
	if r.err != nil {
		return nil, r.err
	}
	return []byte("refresh"), nil
}
func (revealing) Delete(context.Context, string, string) error { return nil }

func TestBitbucketSecrets_reveal(t *testing.T) {
	other := errors.New("vault is down")
	for _, tc := range []struct {
		name string
		err  error
		want error
	}{
		{"missing", cloudconn.ErrNotFound, repositories.ErrBitbucketSecretMissing},
		{"missing, wrapped", fmt.Errorf("reveal: %w", cloudconn.ErrNotFound), repositories.ErrBitbucketSecretMissing},
		{"another failure", other, other},
		{"found", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := bitbucketSecrets{revealing{tc.err}}.Reveal(context.Background(), "aorg_a", "bbws_1")
			if !errors.Is(err, tc.want) || (tc.want == nil && err != nil) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if errors.Is(tc.want, other) && errors.Is(err, repositories.ErrBitbucketSecretMissing) {
				t.Fatalf("another failure read as a missing secret: %v", err)
			}
			if tc.err == nil && string(raw) != "refresh" {
				t.Fatalf("revealed %q", raw)
			}
		})
	}
}

// A consumer Bitbucket turned down is the operator's configuration, so it
// answers the way a consumer that isn't set up does.
func TestFail_rejectedBitbucketConsumer(t *testing.T) {
	for _, err := range []error{
		repositories.ErrBitbucketConsumerRejected,
		fmt.Errorf("renew the Bitbucket sign-in for acme: %w", repositories.ErrBitbucketConsumerRejected),
	} {
		var got interface{ StatusCode() int }
		mapped := fail(err)
		if !errors.As(mapped, &got) || got.StatusCode() != http.StatusConflict {
			t.Fatalf("fail(%v) = %v, want a 409", err, mapped)
		}
		if mapped.Error() != repositories.ErrBitbucketConsumerRejected.Error() {
			t.Fatalf("fail(%v) says %q", err, mapped.Error())
		}
	}
	var unset interface{ StatusCode() int }
	if !errors.As(fail(repositories.ErrBitbucketNotConfigured), &unset) || unset.StatusCode() != http.StatusConflict {
		t.Fatal("a consumer that isn't set up should answer 409 too")
	}
}
