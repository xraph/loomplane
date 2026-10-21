package recovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/backupfiles"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/security"
)

var retentionNow = time.Date(2026, time.October, 20, 12, 0, 0, 0, time.UTC)

func retentionBackup(t *testing.T, id string, age int, verified bool) Backup {
	t.Helper()
	recipe, err := recipes.Lookup("postgres", "17", "arm64")
	if err != nil {
		t.Fatal(err)
	}
	config, err := json.Marshal(planning.ResourceConfig{Provider: "server", Kind: "postgres", Target: "connection", Catalog: recipe})
	if err != nil {
		t.Fatal(err)
	}
	completed := retentionNow.Add(-time.Duration(age) * 24 * time.Hour)
	b := Backup{ID: id, OrgID: "org", ProjectID: "project", EnvironmentID: "environment", Name: "database", Status: "succeeded", CreatedAt: completed.Add(-time.Minute), CompletedAt: &completed,
		Source: planning.Binding{ID: "binding", OrgID: "org", ProjectID: "project", EnvironmentID: "environment", Name: "database", Subject: planning.ResourceSubject, ExternalID: "instance", Provider: "server", ConnectionID: "connection", Config: config},
		Object: backupfiles.Object{Key: "org/project/environment/" + id + "/attempt.dump", Size: 100, SHA256: strings.Repeat("a", 64)}, Token: "attempt"}
	if verified {
		checked := completed.Add(time.Minute)
		b.RestoredAt = &checked
	}
	return b
}

func TestRetentionPreviewProtectsNewestTestedAndActivePoints(t *testing.T) {
	points := []RetentionEntry{
		{Backup: retentionBackup(t, "old-untested", 40, false)},
		{Backup: retentionBackup(t, "newest", 10, false)},
		{Backup: retentionBackup(t, "tested", 30, true)},
		{Backup: retentionBackup(t, "older-tested", 35, true)},
		{Backup: retentionBackup(t, "in-use", 45, false), InUse: true},
		{Backup: retentionBackup(t, "failed-newest", 1, false)},
	}
	points[5].Backup.Status = "failed"
	preview, err := previewRetention(retentionNow, RetentionInput{OlderThanDays: 7, KeepLast: 1}, &points[0].Backup, points)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"old-untested": "eligible", "newest": "newest", "tested": "last_verified", "older-tested": "eligible", "in-use": "active_restore", "failed-newest": "not_complete"}
	for _, p := range preview.Points {
		if p.Reason != want[p.BackupID] || p.Eligible != (p.Reason == "eligible") {
			t.Fatalf("unsafe retention decision: %+v", p)
		}
		delete(want, p.BackupID)
	}
	if len(want) != 0 || !preview.Complete || preview.EligibleCount != 2 || preview.EligibleBytes != 200 {
		t.Fatalf("incomplete preview: %+v, missing %+v", preview, want)
	}
	if points[0].Backup.ID != "old-untested" {
		t.Fatal("preview reordered its caller's inventory")
	}
}

func TestRetentionPreviewRequiresRestoreEvidenceAndCompleteInventory(t *testing.T) {
	for _, count := range []int{3, 1001} {
		points := make([]RetentionEntry, count)
		for i := range points {
			points[i].Backup = retentionBackup(t, fmt.Sprintf("point-%04d", i), 30+i, count > 3)
		}
		preview, err := previewRetention(retentionNow, RetentionInput{OlderThanDays: 7, KeepLast: 1}, &points[0].Backup, points)
		if err != nil {
			t.Fatal(err)
		}
		if preview.EligibleCount != 0 || preview.EligibleBytes != 0 || preview.Complete != (count <= 1000) {
			t.Fatalf("unsafe incomplete inventory or untested backups: %+v", preview)
		}
	}
}

func TestRetentionPreviewUsesCompletionAgeAndKeepsRequestedCount(t *testing.T) {
	points := []RetentionEntry{
		{Backup: retentionBackup(t, "boundary", 7, false)},
		{Backup: retentionBackup(t, "protected-count", 9, false)},
		{Backup: retentionBackup(t, "old", 20, false)},
		{Backup: retentionBackup(t, "verified", 40, true)},
	}
	points[0].Backup.CreatedAt = retentionNow.Add(-90 * 24 * time.Hour)
	preview, err := previewRetention(retentionNow, RetentionInput{OlderThanDays: 7, KeepLast: 2}, &points[0].Backup, points)
	if err != nil || preview.EligibleCount != 1 {
		t.Fatalf("completion/count selection: %+v %v", preview, err)
	}
	for _, p := range preview.Points {
		if p.Eligible && p.BackupID != "old" {
			t.Fatalf("count or completion boundary ignored: %+v", p)
		}
	}
}

func TestRetentionPreviewKeepsNewCompletionsAndExactAgeBoundary(t *testing.T) {
	points := []RetentionEntry{
		{Backup: retentionBackup(t, "newest", 1, false)},
		{Backup: retentionBackup(t, "recent", 3, false)},
		{Backup: retentionBackup(t, "boundary", 7, false)},
		{Backup: retentionBackup(t, "old", 8, false)},
		{Backup: retentionBackup(t, "verified", 20, true)},
	}
	points[1].Backup.CreatedAt = retentionNow.Add(-90 * 24 * time.Hour)
	preview, err := previewRetention(retentionNow, RetentionInput{OlderThanDays: 7, KeepLast: 1}, &points[0].Backup, points)
	if err != nil || preview.EligibleCount != 1 {
		t.Fatalf("completion age ignored: %+v %v", preview, err)
	}
	for _, p := range preview.Points {
		if p.Eligible && p.BackupID != "old" || (p.BackupID == "recent" || p.BackupID == "boundary") && p.Reason != "recent" {
			t.Fatalf("recent completion or boundary expired: %+v", p)
		}
	}
}

func TestRetentionPreviewFailsClosedOnInvalidScopeAndOptions(t *testing.T) {
	valid := RetentionInput{OlderThanDays: 7, KeepLast: 1}
	for _, in := range []RetentionInput{{}, {OlderThanDays: -1, KeepLast: 1}, {OlderThanDays: 3651, KeepLast: 1}, {OlderThanDays: 7, KeepLast: 0}, {OlderThanDays: 7, KeepLast: 1001}} {
		if _, err := previewRetention(retentionNow, in, nil, nil); !errors.Is(err, ErrInvalid) {
			t.Fatalf("unsafe retention settings %+v: %v", in, err)
		}
	}
	for _, change := range []func(*Backup){
		func(b *Backup) { b.OrgID = "other-org" },
		func(b *Backup) { b.Source.ExternalID = "other-instance" },
		func(b *Backup) { b.Source.ConnectionID = "other-connection" },
		func(b *Backup) {
			b.Source.Config = json.RawMessage(`{"provider":"server","kind":"postgres","catalog":{"id":"postgresql-17","backup_format":"wal-chain"}}`)
		},
	} {
		first, other := retentionBackup(t, "first", 40, true), retentionBackup(t, "other", 50, false)
		change(&other)
		if _, err := previewRetention(retentionNow, valid, &first, []RetentionEntry{{Backup: first}, {Backup: other}}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("mixed or unsupported recovery inventory accepted: %v", err)
		}
	}
}

type retentionStore struct {
	*lookupStore
	read    bool
	entries []RetentionEntry
}

func (s *retentionStore) RetentionInventory(context.Context, *Backup) ([]RetentionEntry, error) {
	s.read = true
	return s.entries, nil
}

func TestRetentionPreviewAuthorizesBeforeInventory(t *testing.T) {
	store := &retentionStore{lookupStore: &lookupStore{record: &Backup{ID: "backup", ProjectID: "project"}}}
	s := Service{Store: store, Authz: denied{}}
	_, err := s.PreviewRetention(t.Context(), security.Principal{OrgID: "requesting-org"}, "backup", RetentionInput{OlderThanDays: 7, KeepLast: 1})
	if !errors.Is(err, security.ErrForbidden) || store.org != "requesting-org" || store.read {
		t.Fatalf("retention inventory bypassed permission/scope: %+v %v", store, err)
	}
}

func TestRetentionPreviewRejectsForeignAnchorBeforeInventory(t *testing.T) {
	b := retentionBackup(t, "backup", 20, true)
	store := &retentionStore{lookupStore: &lookupStore{record: &b}}
	s := Service{Store: store, Authz: allowed{}}
	_, err := s.PreviewRetention(t.Context(), security.Principal{OrgID: "other-org"}, b.ID, RetentionInput{OlderThanDays: 7, KeepLast: 1})
	if !errors.Is(err, security.ErrForbidden) || store.read {
		t.Fatalf("foreign anchor reached inventory: %+v %v", store, err)
	}
}

func TestRetentionPreviewRejectsInventoryOutsideAuthorizedAnchor(t *testing.T) {
	anchor, foreign := retentionBackup(t, "anchor", 10, true), retentionBackup(t, "foreign-point", 20, false)
	foreign.ProjectID, foreign.Source.ProjectID = "other-project", "other-project"
	store := &retentionStore{lookupStore: &lookupStore{record: &anchor}, entries: []RetentionEntry{{Backup: foreign}}}
	s := Service{Store: store, Authz: allowed{}}
	_, err := s.PreviewRetention(t.Context(), security.Principal{OrgID: anchor.OrgID}, anchor.ID, RetentionInput{OlderThanDays: 7, KeepLast: 1})
	if !errors.Is(err, ErrInvalid) || !store.read {
		t.Fatalf("inventory escaped authorized source: %v", err)
	}
}

func TestRetentionPreviewDoesNotUseInvalidPointsAsReplacements(t *testing.T) {
	for _, change := range []func(*Backup){
		func(b *Backup) { b.Object.SHA256 = "not-a-digest" },
		func(b *Backup) { b.Object.Key = "org/project/other-object.dump" },
		func(b *Backup) { b.Object.Size = -1 },
		func(b *Backup) { b.CreatedAt = retentionNow.Add(time.Hour) },
		func(b *Backup) { b.CompletedAt = nil },
	} {
		newest, older := retentionBackup(t, "newest", 10, true), retentionBackup(t, "older", 20, false)
		change(&newest)
		preview, err := previewRetention(retentionNow, RetentionInput{OlderThanDays: 7, KeepLast: 1}, &newest, []RetentionEntry{{Backup: newest}, {Backup: older}})
		if err != nil || preview.EligibleCount != 0 {
			t.Fatalf("invalid new point allowed retirement: %+v %v", preview, err)
		}
	}
}

func TestRetentionPreviewReportsDeletionCapabilityPerCapturedPoint(t *testing.T) {
	s, _, records := retirementWorkerFixture(t)
	s.Authz = allowed{}
	candidate := records.points["candidate"]
	candidate.Status = "succeeded"
	legacy := *candidate
	legacy.ID = "legacy-point"
	legacy.Object.Key = "org/project/environment/legacy-point/attempt.dump"
	legacy.Object.DestinationID = ""
	moved := *candidate
	moved.ID = "moved-point"
	moved.Object.Key = "org/project/environment/moved-point/attempt.dump"
	moved.Object.DestinationID = strings.Repeat("f", 64)
	s.Store = &retentionStore{lookupStore: &lookupStore{record: candidate}, entries: []RetentionEntry{{Backup: *candidate}, {Backup: *records.points["witness"]}, {Backup: legacy}, {Backup: moved}}}
	preview, err := s.PreviewRetention(t.Context(), security.Principal{OrgID: "org"}, candidate.ID, RetentionInput{OlderThanDays: 7, KeepLast: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, point := range preview.Points {
		supported := point.BackupID == "candidate" || point.BackupID == "witness"
		if point.DeletionSupported != supported || !supported && point.DeletionUnavailableReason == "" {
			t.Fatalf("misreported deletion capability: %+v", point)
		}
	}
	s.Files = nil
	preview, err = s.PreviewRetention(t.Context(), security.Principal{OrgID: "org"}, candidate.ID, RetentionInput{OlderThanDays: 7, KeepLast: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, point := range preview.Points {
		if point.DeletionSupported || point.DeletionUnavailableReason == "" {
			t.Fatalf("unconfigured storage offered deletion: %+v", point)
		}
	}
}

func TestRetentionPreviewProtectsSourceUntilEarlierRetirementFinishes(t *testing.T) {
	points := []RetentionEntry{{Backup: retentionBackup(t, "pending", 40, false)}, {Backup: retentionBackup(t, "otherwise-eligible", 50, false)}, {Backup: retentionBackup(t, "witness", 1, true)}}
	points[0].Backup.Status = "retiring"
	preview, err := previewRetention(retentionNow, RetentionInput{OlderThanDays: 7, KeepLast: 1}, &points[1].Backup, points)
	if err != nil {
		t.Fatal(err)
	}
	for _, point := range preview.Points {
		if point.BackupID == "otherwise-eligible" && (point.Eligible || point.Reason != "retirement_active") {
			t.Fatalf("earlier retirement was invisible in preview: %+v", point)
		}
	}
}
