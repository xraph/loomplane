package plane

import (
	"testing"

	"github.com/jackc/pgx/v5/pgproto3"
)

func TestJudge(t *testing.T) {
	var (
		rfq      = &pgproto3.ReadyForQuery{TxStatus: 'I'}
		cc       = &pgproto3.CommandComplete{CommandTag: []byte("SELECT 1")}
		row      = &pgproto3.DataRow{}
		rowDesc  = &pgproto3.RowDescription{}
		noData   = &pgproto3.NoData{}
		params   = &pgproto3.ParameterDescription{}
		copyIn   = &pgproto3.CopyInResponse{}
		suspend  = &pgproto3.PortalSuspended{}
		empty    = &pgproto3.EmptyQueryResponse{}
		fnResult = &pgproto3.FunctionCallResponse{}
	)
	cases := []struct {
		kind entryKind
		msg  pgproto3.BackendMessage
		want verdict
	}{
		{entryQuery, rowDesc, verdictPass},
		{entryQuery, row, verdictPass},
		{entryQuery, cc, verdictPass},
		{entryQuery, empty, verdictPass},
		{entryQuery, copyIn, verdictPass},
		{entryQuery, rfq, verdictDone},
		{entryQuery, &pgproto3.ParseComplete{}, verdictUnexpected},
		{entrySync, rfq, verdictDone},
		{entrySync, cc, verdictUnexpected},
		{entryFunctionCall, fnResult, verdictPass},
		{entryFunctionCall, rfq, verdictDone},
		{entryParse, &pgproto3.ParseComplete{}, verdictDone},
		{entryParse, &pgproto3.BindComplete{}, verdictUnexpected},
		{entryBind, &pgproto3.BindComplete{}, verdictDone},
		{entryClose, &pgproto3.CloseComplete{}, verdictDone},
		{entryDescribeStatement, params, verdictPass},
		{entryDescribeStatement, rowDesc, verdictDone},
		{entryDescribeStatement, noData, verdictDone},
		{entryDescribePortal, rowDesc, verdictDone},
		{entryDescribePortal, noData, verdictDone},
		{entryDescribePortal, params, verdictUnexpected},
		{entryExecute, row, verdictPass},
		{entryExecute, copyIn, verdictPass},
		{entryExecute, cc, verdictDone},
		{entryExecute, empty, verdictDone},
		{entryExecute, suspend, verdictDone},
		{entryExecute, rfq, verdictUnexpected},
		{entryExecute, rowDesc, verdictUnexpected},
	}
	for _, tc := range cases {
		head := &entry{kind: tc.kind}
		if got := judge(head, tc.msg); got != tc.want {
			t.Errorf("judge(%v, %s) = %v, want %v", tc.kind, showBackend(tc.msg), got, tc.want)
		}
	}

	// Notices, parameter changes and notifications belong to no request.
	for _, m := range []pgproto3.BackendMessage{&pgproto3.NoticeResponse{}, &pgproto3.ParameterStatus{}, &pgproto3.NotificationResponse{}} {
		if got := judge(nil, m); got != verdictAsync {
			t.Errorf("judge(nil, %s) = %v, want async", showBackend(m), got)
		}
		if got := judge(&entry{kind: entryExecute}, m); got != verdictAsync {
			t.Errorf("judge(Execute, %s) = %v, want async", showBackend(m), got)
		}
	}
	if got := judge(&entry{kind: entryBind}, &pgproto3.ErrorResponse{}); got != verdictError {
		t.Errorf("judge(Bind, ErrorResponse) = %v, want error", got)
	}
	if got := judge(nil, rfq); got != verdictUnexpected {
		t.Errorf("judge(nil, ReadyForQuery) = %v, want unexpected", got)
	}
}

func TestSequencerFail(t *testing.T) {
	var q sequencer
	for _, k := range []entryKind{entryBind, entryExecute, entrySynthetic, entryQuery, entrySync, entryBind, entrySync} {
		q.push(entry{kind: k})
	}
	dropped, synced := q.fail()
	if len(dropped) != 4 || !synced {
		t.Fatalf("fail dropped %d entries, synced %v; want the 4 before the first Sync", len(dropped), synced)
	}
	if q.len() != 3 || q.head().kind != entrySync {
		t.Fatalf("after fail: %d entries, head %v; want the Sync at the head", q.len(), q.head().kind)
	}

	// With no Sync queued, everything goes.
	q = sequencer{}
	q.push(entry{kind: entryParse})
	q.push(entry{kind: entryDescribeStatement})
	if dropped, synced := q.fail(); len(dropped) != 2 || synced || !q.empty() {
		t.Fatalf("fail without a Sync: dropped %d, synced %v, %d left", len(dropped), synced, q.len())
	}
}

func TestSequencerFIFO(t *testing.T) {
	var q sequencer
	if !q.empty() || q.head() != nil {
		t.Fatal("a new sequencer is not empty")
	}
	q.push(entry{kind: entryParse})
	q.push(entry{kind: entryBind})
	if e := q.pop(); e.kind != entryParse {
		t.Fatalf("pop = %v, want the first entry", e.kind)
	}
	if e := q.pop(); e.kind != entryBind || !q.empty() {
		t.Fatalf("pop = %v, empty %v", e.kind, q.empty())
	}
}

func TestStatementName(t *testing.T) {
	a := statementName("SELECT $1", []uint32{23})
	if len(a) != len("lp_")+12 || a[:3] != "lp_" {
		t.Fatalf("statementName = %q, want lp_ and 12 hex characters", a)
	}
	if a != statementName("SELECT $1", []uint32{23}) {
		t.Fatal("statementName is not stable")
	}
	if a == statementName("SELECT $1", []uint32{25}) || a == statementName("SELECT $1", nil) || a == statementName("SELECT $2", []uint32{23}) {
		t.Fatal("different statements share a backend name")
	}
}
