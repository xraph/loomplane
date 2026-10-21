//go:build integration

package integration

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/cloudprofiles"
)

func TestCloudMetadataRetainedSyncGuard(t *testing.T) {
	st, _, _ := cloudSyncDatabase(t)
	p, snap, s := persistAssignmentPreview(t, st, "metadata", "cloud", 0)
	accepted, err := st.Accept(t.Context(), p, snap, s, "metadata")
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{cloudprofiles.SyncPending, cloudprofiles.SyncRunning, cloudprofiles.SyncFailed, cloudprofiles.SyncRouting, cloudprofiles.SyncCleanup, cloudprofiles.SyncReverting} {
		accepted.Status = status
		if err = st.SaveSync(t.Context(), accepted, snap); err != nil {
			t.Fatal(err)
		}
		target, e := st.GetTarget(t.Context(), "org", "proj", "cloud", "target")
		if e != nil {
			t.Fatal(e)
		}
		target.DisplayName, target.Revision = "Renamed", target.Revision+1
		if e = st.UpdateTarget(t.Context(), target, target.Revision-1); !errors.Is(e, cloudprofiles.ErrBusy) {
			t.Fatalf("%s target edit invalidated recovery: %v", status, e)
		}
		if !strings.Contains(e.Error(), "finish or revert") {
			t.Fatalf("rename conflict omitted recovery action: %v", e)
		}
		profile, e := st.GetProfile(t.Context(), "org", "proj", "cloud")
		if e != nil {
			t.Fatal(e)
		}
		profile.Name, profile.Revision = "Renamed", profile.Revision+1
		if e = st.UpdateProfile(t.Context(), profile, profile.Revision-1); !errors.Is(e, cloudprofiles.ErrBusy) {
			t.Fatalf("%s Cloud edit invalidated recovery: %v", status, e)
		}
	}
	accepted.Status = cloudprofiles.SyncSucceeded
	if err = st.SaveSync(t.Context(), accepted, snap); err != nil {
		t.Fatal(err)
	}
	target, err := st.GetTarget(t.Context(), "org", "proj", "cloud", "target")
	if err != nil {
		t.Fatal(err)
	}
	target.DisplayName, target.Revision = "Renamed", target.Revision+1
	if err = st.UpdateTarget(t.Context(), target, target.Revision-1); err != nil {
		t.Fatal(err)
	}
	profile, err := st.GetProfile(t.Context(), "org", "proj", "cloud")
	if err != nil {
		t.Fatal(err)
	}
	profile.Name, profile.Revision = "Renamed", profile.Revision+1
	if err = st.UpdateProfile(t.Context(), profile, profile.Revision-1); err != nil {
		t.Fatal(err)
	}
}

func TestCloudMetadataAcceptanceRace(t *testing.T) {
	for _, kind := range []string{"target", "profile"} {
		for i := range 5 {
			t.Run(fmt.Sprintf("%s/%d", kind, i), func(t *testing.T) {
				st, conn, _ := cloudSyncDatabase(t)
				p, snap, s := persistAssignmentPreview(t, st, "race", "cloud", 0)
				snap.ProfileRevisions = map[string]int64{"cloud": 1}
				snap.TargetRevisions = map[string]int64{"target": 1}
				target, err := st.GetTarget(t.Context(), "org", "proj", "cloud", "target")
				if err != nil {
					t.Fatal(err)
				}
				profile, err := st.GetProfile(t.Context(), "org", "proj", "cloud")
				if err != nil {
					t.Fatal(err)
				}
				target.Revision, profile.Revision = 2, 2
				target.DisplayName, profile.Name = "Renamed", "Renamed"
				tx, err := conn.Begin(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = tx.Rollback(t.Context()) }()
				if _, err = tx.Exec(t.Context(), `SELECT id FROM loomplane_projects WHERE id='proj' FOR UPDATE`); err != nil {
					t.Fatal(err)
				}
				accepted, edited := make(chan error, 1), make(chan error, 1)
				go func() { _, e := st.Accept(t.Context(), p, snap, s, "race"); accepted <- e }()
				go func() {
					if kind == "target" {
						edited <- st.UpdateTarget(t.Context(), target, 1)
					} else {
						edited <- st.UpdateProfile(t.Context(), profile, 1)
					}
				}()
				if err = tx.Commit(t.Context()); err != nil {
					t.Fatal(err)
				}
				acceptErr, editErr := <-accepted, <-edited
				if acceptErr == nil {
					if !errors.Is(editErr, cloudprofiles.ErrBusy) {
						t.Fatalf("accepted sync allowed concurrent %s edit: %v", kind, editErr)
					}
				} else if !errors.Is(acceptErr, cloudprofiles.ErrConflict) || editErr != nil {
					t.Fatalf("race must accept one operation: accept=%v edit=%v", acceptErr, editErr)
				}
			})
		}
	}
}
