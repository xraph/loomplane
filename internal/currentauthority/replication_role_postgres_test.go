package currentauthority

import (
	"context"
	"errors"
	"testing"
)

func TestFleetPostgresRejectsReplicaQualification(t *testing.T) {
	f := newOrdinaryPGFixture(t)
	for _, mode := range []string{"origin", "replica"} {
		t.Run(mode, func(t *testing.T) {
			installed := newOrdinaryInstallationPG(t, f)
			var err error
			installed.dsn, err = withDSNParams(installed.dsn, map[string]string{"session_replication_role": mode})
			if err != nil {
				t.Fatal(err)
			}
			report, err := installed.install(t, installed.authorizer, false)
			if mode == "origin" {
				if err != nil || report.Fleet != ProfileReady || report.Ordinary != ProfileReady {
					t.Fatalf("origin qualification=%+v %v", report, err)
				}
				installed.assertPools(t, 2)
				return
			}
			if !errors.Is(err, ErrUnavailable) || installed.coordinator.installation != nil {
				t.Fatalf("replica qualification=%+v %v", report, err)
			}
			installed.assertPools(t, 0)
		})
	}
}

func TestFleetPostgresWriterRequiresOriginOnEveryTransaction(t *testing.T) {
	f := newOrdinaryPGFixture(t)
	installed := newOrdinaryInstallationPG(t, f)
	if report, err := installed.install(t, installed.authorizer, false); err != nil || report.Fleet != ProfileReady || report.Ordinary != ProfileReady {
		t.Fatalf("origin installation=%+v %v", report, err)
	}
	writer := installed.coordinator.installation.backend.writer
	callbackResult := errors.New("matching origin callback")
	var previousPID uint32
	for _, tc := range []struct {
		name, mode string
		replace    bool
	}{
		{"initial origin", "origin", false},
		{"reused replica", "replica", false},
		{"replacement replica", "replica", true},
		{"origin restored", "origin", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.replace {
				writer.Reset()
			}
			conn, err := writer.Acquire(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			pid := conn.Conn().PgConn().PID()
			_, err = conn.Exec(t.Context(), `SELECT pg_catalog.set_config('session_replication_role',$1,false)`, tc.mode)
			conn.Release()
			if err != nil {
				t.Fatal(err)
			}
			if previousPID != 0 && ((tc.replace && pid == previousPID) || (!tc.replace && pid != previousPID)) {
				t.Fatalf("unexpected writer session: before=%d after=%d replacement=%t", previousPID, pid, tc.replace)
			}
			previousPID = pid
			for _, kind := range []string{"ordinary", "fleet"} {
				t.Run(kind, func(t *testing.T) {
					entered := false
					work := func(context.Context, *Attempt) error { entered = true; return callbackResult }
					var runErr error
					if kind == "ordinary" {
						runErr = installed.ordinary.RunOrdinary(t.Context(), f.binding, work)
					} else {
						binding := fleetTestBinding()
						binding.AppID = f.binding.AppID
						runErr = installed.fleet.RunFleet(t.Context(), binding, work)
					}
					if tc.mode == "origin" {
						if !entered || !errors.Is(runErr, callbackResult) {
							t.Fatalf("origin transaction entered=%t err=%v", entered, runErr)
						}
					} else if entered || !errors.Is(runErr, ErrUnavailable) {
						t.Fatalf("replica transaction entered=%t err=%v", entered, runErr)
					}
				})
			}
			conn, err = writer.Acquire(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			var actual string
			err = conn.QueryRow(t.Context(), `SELECT pg_catalog.current_setting('session_replication_role')`).Scan(&actual)
			conn.Release()
			if err != nil || actual != tc.mode {
				t.Fatalf("writer mode silently changed: got=%q want=%q err=%v", actual, tc.mode, err)
			}
		})
	}
}
