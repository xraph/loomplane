//go:build integration

package realtime

import (
	"encoding/json"
	"testing"
)

func TestSnapshot_scopesRecordsAndReplaysAfterAtomicBoundary(t *testing.T) {
	s, db, _ := eventDatabase(t)
	if _, err := db.Exec(t.Context(), `INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('proj_a','org_a','A','a','active','usr_a',now(),now()),('proj_b','org_b','B','b','active','usr_b',now(),now())`); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.Snapshot(t.Context(), "org_a", nil, nil)
	if err != nil || snapshot == nil || len(snapshot.Records) != 1 {
		t.Fatalf("snapshot: %+v %v", snapshot, err)
	}
	var project struct{ ID, Name string }
	if err = json.Unmarshal(snapshot.Records[0].Data, &project); err != nil || project.ID != "proj_a" {
		t.Fatalf("org scope: %+v %v", project, err)
	}
	if _, err = db.Exec(t.Context(), `UPDATE loomplane_projects SET name='After boundary' WHERE id='proj_a'`); err != nil {
		t.Fatal(err)
	}
	events, found, err := s.Events(t.Context(), "org_a", snapshot.Cursor, 10)
	if err != nil || !found || len(events) != 1 {
		t.Fatalf("boundary replay: %+v %v %v", events, found, err)
	}
	if err = json.Unmarshal(events[0].Data, &project); err != nil || project.Name != "After boundary" {
		t.Fatalf("replayed data: %+v %v", project, err)
	}
	filtered, err := s.Snapshot(t.Context(), "org_a", []string{"proj_b"}, nil)
	if err != nil || len(filtered.Records) != 0 {
		t.Fatalf("foreign project scope: %+v %v", filtered, err)
	}
}
