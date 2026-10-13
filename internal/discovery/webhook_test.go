package discovery

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"testing"
)

func TestService_startFromPushAnalyzesTheSourcesTrackedBranch(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, 2)
	src, err := f.store.GetSource(ctx, org, project, "mono")
	if err != nil {
		t.Fatal(err)
	}
	a, err := f.svc.StartFromPush(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	if a.CreatedBy != PushActor || a.Source != "mono" || a.SourceID != src.ID || a.Ref != "" || a.Repository != "acme/mono" {
		t.Fatalf("analysis %+v", a)
	}
	if len(f.queue.ids) != 1 || f.queue.ids[0] != a.ID {
		t.Fatalf("queued %v", f.queue.ids)
	}
	// One already under way is left alone.
	if _, err := f.svc.StartFromPush(ctx, src); !errors.Is(err, ErrAlreadyActive) {
		t.Fatalf("a second push while one runs: %v", err)
	}
}

func sign(secret, body []byte) string {
	m := hmac.New(sha256.New, secret)
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

func push(ref string, installation int64, private bool) []byte {
	inst := "null"
	if installation != 0 {
		inst = fmt.Sprintf(`{"id": %d}`, installation)
	}
	return fmt.Appendf(nil, `{"ref": %q, "after": "abc", "deleted": false,
		"repository": {"full_name": "acme/mono", "default_branch": "main", "private": %t},
		"installation": %s}`, ref, private, inst)
}

func TestWebhooks_aPushReanalyzesEverySourceTrackingTheDefaultBranch(t *testing.T) {
	ctx := context.Background()
	sources := []*Source{
		// Two projects in the organization that connected the installation
		// track acme/mono, one spelled in another case.
		{ID: "src_a", OrgID: "org_a", ProjectID: "proj_a", Alias: "mono", Repository: "acme/mono"},
		{ID: "src_b", OrgID: "org_a", ProjectID: "proj_b", Alias: "api", Repository: "Acme/Mono", Ref: "main"},
		// One tracks another branch, which didn't move.
		{ID: "src_c", OrgID: "org_a", ProjectID: "proj_c", Alias: "mono", Repository: "acme/mono", Ref: "release"},
		// One is in an organization that didn't connect the installation.
		{ID: "src_d", OrgID: "org_b", ProjectID: "proj_d", Alias: "mono", Repository: "acme/mono"},
		// One is already being analyzed.
		{ID: "src_e", OrgID: "org_a", ProjectID: "proj_e", Alias: "mono", Repository: "acme/mono"},
	}
	var started []string
	w := &Webhooks{
		Secret: []byte("s3cret"),
		Installations: func(_ context.Context, id int64) ([]string, error) {
			if id != 7 {
				return nil, nil
			}
			return []string{"org_a"}, nil
		},
		Sources: func(_ context.Context, fullName string) ([]*Source, error) {
			if fullName != "acme/mono" {
				t.Fatalf("looked up %q", fullName)
			}
			return sources, nil
		},
		Start: func(_ context.Context, src *Source) (*Analysis, error) {
			if src.ID == "src_e" {
				return nil, fmt.Errorf("%w: %s", ErrAlreadyActive, src.Alias)
			}
			started = append(started, src.ID)
			return &Analysis{ID: "anl_" + src.ID}, nil
		},
	}

	body := push("refs/heads/main", 7, true)
	if err := w.Verify(sign([]byte("s3cret"), body), body); err != nil {
		t.Fatal(err)
	}
	if err := w.Verify(sign([]byte("wrong"), body), body); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("a forged signature: %v", err)
	}
	n, err := w.Handle(ctx, "push", body)
	if err != nil || n != 2 || !slices.Equal(started, []string{"src_a", "src_b"}) {
		t.Fatalf("started %d %v, %v", n, started, err)
	}

	// A public repository's own webhook reaches every organization.
	started = nil
	if n, err = w.Handle(ctx, "push", push("refs/heads/main", 0, false)); err != nil || n != 3 ||
		!slices.Equal(started, []string{"src_a", "src_b", "src_d"}) {
		t.Fatalf("public push started %d %v, %v", n, started, err)
	}

	// Another branch, a private repository without an installation, and
	// other events do nothing.
	started = nil
	for _, tt := range []struct {
		event string
		body  []byte
	}{
		{"push", push("refs/heads/release", 7, true)},
		{"push", push("refs/heads/main", 0, true)},
		{"ping", body},
	} {
		if n, err := w.Handle(ctx, tt.event, tt.body); err != nil || n != 0 {
			t.Fatalf("%s %s: %d, %v", tt.event, tt.body, n, err)
		}
	}
	if len(started) != 0 {
		t.Fatalf("started %v", started)
	}
}
