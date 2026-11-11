package fleets

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/fleet"
)

func TestFleetEventCursorBounded(t *testing.T) {
	cursor := eventCursor("instance-a", 42)
	seq, limit, err := eventPageInput("instance-a", cursor, 0)
	if err != nil || seq != 42 || limit != 50 {
		t.Fatalf("cursor roundtrip: %d %d %v", seq, limit, err)
	}
	for _, input := range []struct {
		id, after string
		limit     int
	}{
		{"instance-b", cursor, 50}, {"instance-a", "-1", 50}, {"instance-a", strings.Repeat("x", 1024), 50}, {"instance-a", "", 201}, {"instance-a", "", -1},
	} {
		if _, _, err = eventPageInput(input.id, input.after, input.limit); err == nil {
			t.Fatalf("accepted bad cursor: %+v", input)
		}
	}
}
func TestFleetEventRedaction(t *testing.T) {
	draft := EventDraft{ID: "e0b774b5-9fe6-46ef-bb24-e679586927f5", InstanceID: "a15abc36-0f62-40f7-81a9-81c843c09773", Generation: 1, Kind: "lifecycle", Lifecycle: &fleet.LifecycleEvent{State: "ready"}}
	if err := validateEventDraft(draft); err != nil {
		t.Fatal(err)
	}
	draft.Lifecycle.Reason = "provider returned credential=synthetic-secret"
	if err := validateEventDraft(draft); err == nil {
		t.Fatal("accepted raw provider error")
	}
	draft.Lifecycle.Reason = ""
	draft.Scaling = &fleet.ScalingEvent{Service: "web", PolicyRevision: 1, Writer: "hpa"}
	if err := validateEventDraft(draft); err == nil {
		t.Fatal("accepted conflicting payloads")
	}
}

func TestFleetEventReleasePinsSeparateTargetGeneration(t *testing.T) {
	d := EventDraft{ID: "event", InstanceID: "instance", Generation: 3, Kind: "release", Release: &fleet.ReleaseEvent{CampaignID: "campaign", State: "paused", Reason: "generation_conflict", TargetGeneration: 2, TargetOperationID: "operation"}}
	if err := validateEventDraft(d); err != nil {
		t.Fatal(err)
	}
	d.Release.TargetGeneration = 0
	if err := validateEventDraft(d); err == nil {
		t.Fatal("target operation without generation accepted")
	}
}

type eventFixtureStore struct{ readFixtureStore }

func (eventFixtureStore) ListEvents(_ context.Context, scope FleetScope, id string, after int64, limit int) (*fleet.EventPage, error) {
	if scope.OrgID != "org" || scope.ProjectID != "project" || scope.FleetID != "fleet" || id != "instance" || after != 0 || limit != 50 {
		return nil, ErrInvalid
	}
	return &fleet.EventPage{Items: []fleet.Event{{ID: "event", InstanceID: id, Sequence: 1}}}, nil
}
func TestFleetEventAccessRevoked(t *testing.T) {
	keys := &readVerifier{}
	s := NewService(Deps{Store: eventFixtureStore{}, Authz: readAuthorizer{}, Keys: keys})
	p := security.Principal{OrgID: "org", UserID: security.FleetSubject("key"), KeyID: "key", Kind: security.KindFleet, Fleet: &security.FleetScope{ProjectID: "project", FleetID: "fleet"}}
	got, err := s.Events(t.Context(), p, "instance", "", 0)
	if err != nil || len(got.Items) != 1 {
		t.Fatalf("events: %+v %v", got, err)
	}
	keys.err = security.ErrForbidden
	if _, err = s.Events(t.Context(), p, "instance", "", 0); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("revoked replay: %v", err)
	}
}
