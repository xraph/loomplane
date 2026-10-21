//go:build integration

package integration

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/xraph/loomplane/internal/recovery"
)

func TestRetirementListPaginatesEqualTimesWithoutCrossingScope(t *testing.T) {
	conn, store := retirementFixture(t)
	if _, err := conn.Exec(t.Context(), `INSERT INTO loomplane_service_backups(id,org_id,project_id,environment_id,connection_id,name,status,request_id,source,object,token,created_by,created_at,completed_at)
 SELECT 'candidate-'||n,org_id,project_id,environment_id,connection_id,name,'succeeded','candidate-request-'||n,source,jsonb_set(object,'{key}',to_jsonb('o/p/e/candidate-'||n||'/attempt.dump')),token,created_by,created_at,completed_at FROM loomplane_service_backups CROSS JOIN generate_series(1,3) n WHERE id='b'`); err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= 3; n++ {
		request := retirementRequest()
		request.ID, request.BackupID, request.RequestID = fmt.Sprintf("retirement-list-%d", n), fmt.Sprintf("candidate-%d", n), fmt.Sprintf("list-request-%d", n)
		r, err := store.BeginRetirement(t.Context(), request, func(context.Context) error { return nil })
		if err != nil {
			t.Fatal(err)
		}
		claim, err := store.ClaimRetirement(t.Context(), "o", r.ID, fmt.Sprintf("worker-%d", n))
		if err != nil {
			t.Fatal(err)
		}
		if err = store.FinishRetirement(t.Context(), claim, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := conn.Exec(t.Context(), `UPDATE loomplane_backup_retirements SET created_at=date_trunc('second',now())`); err != nil {
		t.Fatal(err)
	}
	first, err := store.ListRetirements(t.Context(), "o", "p", recovery.RetirementListInput{Limit: 2})
	if err != nil || len(first.Retirements) != 2 || first.NextBefore == "" || first.Retirements[0].ID != "retirement-list-3" || first.Retirements[1].ID != "retirement-list-2" {
		t.Fatalf("unstable first page: %+v %v", first, err)
	}
	second, err := store.ListRetirements(t.Context(), "o", "p", recovery.RetirementListInput{Limit: 2, Before: first.NextBefore})
	if err != nil || len(second.Retirements) != 1 || second.NextBefore != "" || second.Retirements[0].ID != "retirement-list-1" {
		t.Fatalf("pagination lost or repeated records: %+v %v", second, err)
	}
	for _, scope := range [][2]string{{"foreign", "p"}, {"o", "foreign-project"}} {
		page, pageErr := store.ListRetirements(t.Context(), scope[0], scope[1], recovery.RetirementListInput{Limit: 2, Before: first.NextBefore})
		if pageErr != nil || len(page.Retirements) != 0 {
			t.Fatalf("cursor crossed scope: %+v %v", page, pageErr)
		}
	}
	for _, in := range []recovery.RetirementListInput{{Before: "bad-cursor"}, {Limit: 101}, {Limit: -1}} {
		if _, err = store.ListRetirements(t.Context(), "o", "p", in); !errors.Is(err, recovery.ErrInvalid) {
			t.Fatalf("invalid page: %+v %v", in, err)
		}
	}
}

func TestRetirementListCanResolveOneBackupWithoutReadingOtherHistories(t *testing.T) {
	_, store := retirementFixture(t)
	r, err := store.BeginRetirement(t.Context(), retirementRequest(), func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	page, err := store.ListRetirements(t.Context(), "o", "p", recovery.RetirementListInput{BackupID: "b"})
	if err != nil || len(page.Retirements) != 1 || page.Retirements[0].ID != r.ID {
		t.Fatalf("point retirement lookup: %+v %v", page, err)
	}
	for _, scope := range [][3]string{{"o", "p", "other-backup"}, {"foreign", "p", "b"}, {"o", "foreign-project", "b"}} {
		page, err = store.ListRetirements(t.Context(), scope[0], scope[1], recovery.RetirementListInput{BackupID: scope[2]})
		if err != nil || len(page.Retirements) != 0 {
			t.Fatalf("point filter crossed scope: %+v %v", page, err)
		}
	}
}
