package plane

import "github.com/jackc/pgx/v5/pgproto3"

// A session forwards frontend messages to whichever backend connection it
// holds, rewrites some of them and answers others itself. The sequencer keeps
// the client's view in order through all of that. Every frontend message that
// expects a reply pushes one entry, in the order the client sent it. A
// backend message belongs to the entry at the head and may complete it.
// Messages the gateway answers itself push a synthetic entry, which is
// answered the moment it reaches the head, so its reply lands exactly where
// the backend's would have.

// entryKind is the frontend message an entry stands for, which fixes the
// backend message that completes it.
type entryKind uint8

const (
	entryQuery             entryKind = iota // simple Query: ReadyForQuery
	entrySync                               // ReadyForQuery
	entryFunctionCall                       // ReadyForQuery
	entryParse                              // ParseComplete
	entryBind                               // BindComplete
	entryClose                              // CloseComplete
	entryDescribeStatement                  // RowDescription or NoData, after ParameterDescription
	entryDescribePortal                     // RowDescription or NoData
	entryExecute                            // CommandComplete, EmptyQueryResponse or PortalSuspended
	entrySynthetic                          // answered by the gateway
)

// extended reports whether the entry is an extended-protocol message, after
// whose failure Postgres skips everything until Sync.
func (k entryKind) extended() bool {
	switch k {
	case entryParse, entryBind, entryClose, entryDescribeStatement, entryDescribePortal, entryExecute:
		return true
	}
	return false
}

// synthKind is what a synthetic entry answers.
type synthKind uint8

const (
	synthParseComplete synthKind = iota + 1 // a Parse of a statement the connection already has
	synthCloseComplete                      // a Close the backend never sees
	synthDeallocate                         // a simple-Query DEALLOCATE of cached statements
)

// entry is one frontend message waiting for its reply.
type entry struct {
	kind entryKind
	// injected marks a Parse the gateway sent on its own, ahead of a Bind or
	// Describe. Its ParseComplete is not the client's to see.
	injected bool
	// stmt is the backend statement a forwarded Parse creates, and unnamed
	// marks a Parse of the unnamed statement. Both are undone if the Parse
	// fails or is skipped.
	stmt    string
	unnamed bool
	// client and prep are the registry entry a client's Parse created, or the
	// one a Close or DEALLOCATE names.
	client string
	prep   *prepared
	// effect classifies the statement a Query or Execute runs. upTo is the
	// last registry entry a DEALLOCATE ALL or DISCARD ALL covers: statements
	// prepared after it in the pipeline survive.
	effect Classification
	upTo   uint64
	synth  synthKind
}

// sequencer is the FIFO of entries.
type sequencer struct{ q []entry }

func (s *sequencer) push(e entry) { s.q = append(s.q, e) }
func (s *sequencer) len() int     { return len(s.q) }
func (s *sequencer) empty() bool  { return len(s.q) == 0 }

func (s *sequencer) head() *entry {
	if len(s.q) == 0 {
		return nil
	}
	return &s.q[0]
}

// onlyHead reports whether every entry behind the head is synthetic, so that
// completing the head leaves nothing for the backend to answer.
func (s *sequencer) onlyHead() bool {
	for i := 1; i < len(s.q); i++ {
		if s.q[i].kind != entrySynthetic {
			return false
		}
	}
	return true
}

func (s *sequencer) pop() entry {
	e := s.q[0]
	s.q[0] = entry{}
	s.q = s.q[1:]
	return e
}

// fail handles an ErrorResponse that ends an extended-protocol message: it
// drops the head and every later entry, real or synthetic, up to and not
// including the next Sync, because Postgres skips those messages too. synced
// is false when no Sync is queued yet; the session then skips the client's
// messages itself until one arrives.
func (s *sequencer) fail() (dropped []entry, synced bool) {
	i := 0
	for i < len(s.q) && s.q[i].kind != entrySync {
		i++
	}
	dropped = append(dropped, s.q[:i]...)
	clear(s.q[:i])
	s.q = s.q[i:]
	return dropped, len(s.q) > 0
}

// verdict is what a backend message means for the head entry.
type verdict uint8

const (
	verdictPass       verdict = iota // part of the head's reply; the head stays
	verdictDone                      // completes the head
	verdictAsync                     // belongs to no request: a notice, parameter change or notification
	verdictError                     // an ErrorResponse; what it does depends on the head
	verdictUnexpected                // the backend and the gateway disagree about the protocol state
)

// judge applies the completion rules to msg for the head entry, which is nil
// when nothing is pending. COPY sub-protocol messages pass inside a Query or
// an Execute.
func judge(head *entry, msg pgproto3.BackendMessage) verdict {
	switch msg.(type) {
	case *pgproto3.NoticeResponse, *pgproto3.ParameterStatus, *pgproto3.NotificationResponse:
		return verdictAsync
	case *pgproto3.ErrorResponse:
		return verdictError
	}
	if head == nil {
		return verdictUnexpected
	}
	switch head.kind {
	case entryQuery:
		switch msg.(type) {
		case *pgproto3.ReadyForQuery:
			return verdictDone
		case *pgproto3.RowDescription, *pgproto3.DataRow, *pgproto3.CommandComplete, *pgproto3.EmptyQueryResponse,
			*pgproto3.CopyInResponse, *pgproto3.CopyOutResponse, *pgproto3.CopyData, *pgproto3.CopyDone:
			return verdictPass
		}
	case entrySync:
		if _, ok := msg.(*pgproto3.ReadyForQuery); ok {
			return verdictDone
		}
	case entryFunctionCall:
		switch msg.(type) {
		case *pgproto3.ReadyForQuery:
			return verdictDone
		case *pgproto3.FunctionCallResponse:
			return verdictPass
		}
	case entryParse:
		if _, ok := msg.(*pgproto3.ParseComplete); ok {
			return verdictDone
		}
	case entryBind:
		if _, ok := msg.(*pgproto3.BindComplete); ok {
			return verdictDone
		}
	case entryClose:
		if _, ok := msg.(*pgproto3.CloseComplete); ok {
			return verdictDone
		}
	case entryDescribeStatement:
		switch msg.(type) {
		case *pgproto3.ParameterDescription:
			return verdictPass
		case *pgproto3.RowDescription, *pgproto3.NoData:
			return verdictDone
		}
	case entryDescribePortal:
		switch msg.(type) {
		case *pgproto3.RowDescription, *pgproto3.NoData:
			return verdictDone
		}
	case entryExecute:
		switch msg.(type) {
		case *pgproto3.CommandComplete, *pgproto3.EmptyQueryResponse, *pgproto3.PortalSuspended:
			return verdictDone
		case *pgproto3.DataRow, *pgproto3.CopyInResponse, *pgproto3.CopyOutResponse, *pgproto3.CopyData, *pgproto3.CopyDone:
			return verdictPass
		}
	}
	return verdictUnexpected
}
