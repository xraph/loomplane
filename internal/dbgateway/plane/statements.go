package plane

import (
	"cmp"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5/pgproto3"
)

// A client's named prepared statement can't live on one backend connection,
// because the client's next transaction may run on another. The session keeps
// a registry of the client's statements and gives each a backend name derived
// from its text and parameter types, so every client preparing the same
// statement shares one copy per connection. Bind, Describe and Close name the
// backend statement, and when the leased connection lacks it the session
// prepares it there first, hiding that ParseComplete from the client. A
// client's Close of a statement never reaches a backend.
//
// The registry is capped, since each statement's text stays in the gateway
// for as long as the session. Postgres sets no such limit, and the gateway
// can't drop a statement the client may still bind, so a Parse of a new name
// past the cap fails with 54000 and the client can go on once it closes some.
// Drivers that cache statements close the ones they evict.
//
// The unnamed statement keeps its name. The session remembers the last one
// the client prepared and prepares it again on a new connection before
// using it there. Portals pass through untouched: they end with the
// transaction, and so does the lease.

// prepared is a statement in the client's registry.
type prepared struct {
	backend string // lp_..., or "" for the unnamed statement
	query   string
	oids    []uint32
	class   Classification
	seq     uint64 // registry order, for DEALLOCATE ALL in a pipeline
}

// missingStatement is what the session binds or describes when the client
// names a statement it doesn't have: the unnamed statement when it has none,
// or an lp_ name it never prepared. It is never prepared anywhere, so the
// backend fails the way Postgres would rather than run a statement another
// session left on the connection.
const missingStatement = "lp_none"

// The client registry's caps: how many named statements a session may hold,
// and how many bytes of statement text in all.
var (
	maxClientStatements     = 4096
	maxClientStatementBytes = 64 << 20
)

const msgTooManyStatements = "too many prepared statements in this session; close some before preparing more"

// statementName is the backend name of a statement: lp_ and the first 40 hex
// characters of the SHA-256 of its text, a zero byte, and its parameter type
// OIDs as 4-byte big-endian integers.
func statementName(query string, oids []uint32) string {
	h := sha256.New()
	h.Write([]byte(query))
	h.Write([]byte{0})
	var b [4]byte
	for _, oid := range oids {
		binary.BigEndian.PutUint32(b[:], oid)
		h.Write(b[:])
	}
	return "lp_" + hex.EncodeToString(h.Sum(nil))[:40]
}

// parse registers the client's statement and prepares it on the leased
// connection under its backend name, or answers it here when the connection
// already has it.
func (s *session) parse(m *pgproto3.Parse) (bool, error) {
	class := classifyStatement(m.Query)
	l, end, err := s.lockLease(true)
	if l == nil {
		return end, err
	}
	if s.skip {
		s.mu.Unlock()
		return false, nil
	}
	if m.Name != "" && s.overCapLocked(m.Name, m.Query) {
		// The backend fails a Bind of a statement nobody prepared the way
		// Postgres fails a Parse: it aborts a transaction block and skips
		// to Sync. The session swaps in its own error, which in an aborted
		// transaction is the one Postgres gives any Parse there.
		refused := softError("54000", msgTooManyStatements)
		if s.status == 'E' {
			refused = softError("25P02", msgAborted)
		}
		return s.forwardLocked(l, false, &entry{kind: entryBind, refused: refused},
			&pgproto3.Bind{PreparedStatement: missingStatement})
	}
	if pins(class) {
		s.pinLocked()
	}
	s.stmtSeq++
	p := &prepared{query: m.Query, oids: slices.Clone(m.ParameterOIDs), class: class, seq: s.stmtSeq}
	if m.Name == "" {
		s.unnamed, s.unnamedOnLease = p, true
		return s.forwardLocked(l, false, &entry{kind: entryParse, unnamed: true, prep: p}, m)
	}

	p.backend = statementName(m.Query, m.ParameterOIDs)
	s.register(m.Name, p)
	c := l.Conn()
	if c.hasStatement(p.backend) {
		s.answerLocked(entry{kind: entrySynthetic, synth: synthParseComplete, client: m.Name, prep: p})
		return false, nil
	}
	msgs := s.makeRoom(c)
	// Marked now, not at ParseComplete, so a second Parse of the same text
	// in the same pipeline doesn't send it twice. undo clears the mark if
	// the Parse fails or is skipped.
	c.addStatement(p.backend)
	msgs = append(msgs, &pgproto3.Parse{Name: p.backend, Query: m.Query, ParameterOIDs: m.ParameterOIDs})
	return s.forwardLocked(l, false, &entry{kind: entryParse, stmt: p.backend, client: m.Name, prep: p}, msgs...)
}

// overCapLocked reports whether registering a statement under name would
// take the registry past its caps. Replacing a statement counts only the
// difference.
func (s *session) overCapLocked(name, query string) bool {
	n, size := len(s.stmts), s.stmtBytes+len(query)
	if old := s.stmts[name]; old != nil {
		n, size = n-1, size-len(old.query)
	}
	return n >= maxClientStatements || size > maxClientStatementBytes
}

// register puts p in the registry under name.
func (s *session) register(name string, p *prepared) {
	s.unregister(name)
	s.stmts[name] = p
	s.stmtBytes += len(p.query)
}

// unregister removes name from the registry.
func (s *session) unregister(name string) {
	if old := s.stmts[name]; old != nil {
		s.stmtBytes -= len(old.query)
		delete(s.stmts, name)
	}
}

// makeRoom evicts what the connection must give up before it takes one more
// statement, and returns the Closes to send for them, each with its entry
// pushed. Called with s.mu held.
func (s *session) makeRoom(c *BackendConn) []pgproto3.FrontendMessage {
	victims := c.victims()
	msgs := make([]pgproto3.FrontendMessage, 0, len(victims))
	for _, name := range victims {
		c.dropStatement(name)
		s.seq.push(entry{kind: entryClose, injected: true, stmt: name})
		msgs = append(msgs, &pgproto3.Close{ObjectType: 'S', Name: name})
	}
	return msgs
}

func (s *session) bind(m *pgproto3.Bind) (bool, error) {
	l, end, err := s.lockLease(true)
	if l == nil {
		return end, err
	}
	if s.skip {
		s.mu.Unlock()
		return false, nil
	}
	p, name := s.resolve(m.PreparedStatement)
	msgs := s.prepareOn(l, p)
	// The portal carries the statement's effect to its Execute.
	if p != nil && tracked(p.class) {
		s.portals[m.DestinationPortal] = p.class
	} else {
		delete(s.portals, m.DestinationPortal)
	}
	b := *m
	b.PreparedStatement = name
	msgs = append(msgs, &b)
	return s.forwardLocked(l, false, &entry{kind: entryBind}, msgs...)
}

func (s *session) describe(m *pgproto3.Describe) (bool, error) {
	l, end, err := s.lockLease(true)
	if l == nil {
		return end, err
	}
	if s.skip {
		s.mu.Unlock()
		return false, nil
	}
	if m.ObjectType != 'S' {
		return s.forwardLocked(l, false, &entry{kind: entryDescribePortal}, m)
	}
	p, name := s.resolve(m.Name)
	msgs := s.prepareOn(l, p)
	msgs = append(msgs, &pgproto3.Describe{ObjectType: 'S', Name: name})
	return s.forwardLocked(l, false, &entry{kind: entryDescribeStatement}, msgs...)
}

func (s *session) execute(m *pgproto3.Execute) (bool, error) {
	l, end, err := s.lockLease(true)
	if l == nil {
		return end, err
	}
	if s.skip {
		s.mu.Unlock()
		return false, nil
	}
	return s.forwardLocked(l, false, &entry{kind: entryExecute, effect: s.portals[m.Portal], upTo: s.stmtSeq}, m)
}

// closeObject answers a Close of a named statement itself. A Close of the
// unnamed statement or a portal goes to the backend when there is one that
// could hold it.
func (s *session) closeObject(m *pgproto3.Close) (bool, error) {
	s.mu.Lock()
	if s.skip {
		s.mu.Unlock()
		return false, nil
	}
	done := entry{kind: entrySynthetic, synth: synthCloseComplete}
	if m.ObjectType == 'S' && m.Name != "" {
		done.client, done.prep = m.Name, s.stmts[m.Name]
		s.answerLocked(done)
		return false, nil
	}
	l := s.lease
	if m.ObjectType == 'S' {
		s.unnamed = nil
		if l == nil || !s.unnamedOnLease {
			s.answerLocked(done)
			return false, nil
		}
		s.unnamedOnLease = false
	} else {
		delete(s.portals, m.Name)
		if l == nil {
			// Portals end with the transaction, and so does every lease.
			s.answerLocked(done)
			return false, nil
		}
	}
	return s.forwardLocked(l, false, &entry{kind: entryClose}, m)
}

// resolve maps a client statement name to its registry entry, if any, and
// the name to send to the backend.
func (s *session) resolve(name string) (p *prepared, backend string) {
	if name == "" {
		if s.unnamed == nil {
			return nil, missingStatement
		}
		return s.unnamed, ""
	}
	if p := s.stmts[name]; p != nil {
		return p, p.backend
	}
	if strings.HasPrefix(name, "lp_") {
		// The gateway's own namespace: the connection may hold that name for
		// another session.
		return nil, missingStatement
	}
	// Not the gateway's: the backend says it doesn't exist.
	return nil, name
}

// prepareOn returns what to send ahead of a Bind or Describe of p when the
// leased connection lacks it: the Parse, after the Closes of any statements
// it evicts, with their entries pushed. Called with s.mu held.
func (s *session) prepareOn(l *Lease, p *prepared) []pgproto3.FrontendMessage {
	if p == nil {
		return nil
	}
	if p.backend == "" {
		if s.unnamedOnLease {
			return nil
		}
		s.unnamedOnLease = true
		s.seq.push(entry{kind: entryParse, injected: true, unnamed: true})
		return []pgproto3.FrontendMessage{&pgproto3.Parse{Query: p.query, ParameterOIDs: p.oids}}
	}
	c := l.Conn()
	if c.hasStatement(p.backend) {
		return nil
	}
	msgs := s.makeRoom(c)
	c.addStatement(p.backend)
	s.seq.push(entry{kind: entryParse, injected: true, stmt: p.backend})
	return append(msgs, &pgproto3.Parse{Name: p.backend, Query: p.query, ParameterOIDs: p.oids})
}

// deallocateLocked answers a simple-Query DEALLOCATE of statements only the
// gateway knows about. It reports false, with s.mu still held, when the
// backend has to see it: an unknown name, or DEALLOCATE ALL in a pinned
// session, whose connection may hold the client's own PREPAREd statements.
// When it answers, it releases s.mu.
func (s *session) deallocateLocked(c Classification) bool {
	if c.Param == "*" {
		if s.pinned {
			return false
		}
	} else if s.stmts[c.Param] == nil {
		return false
	}
	s.answerLocked(entry{kind: entrySynthetic, synth: synthDeallocate, client: c.Param, prep: s.stmts[c.Param], upTo: s.stmtSeq})
	return true
}

// reply is the gateway's answer to a synthetic entry. Registry changes wait
// until here, so a Close or DEALLOCATE skipped after an error leaves the
// statement in place, as Postgres would.
func (s *session) reply(e entry) []pgproto3.BackendMessage {
	switch e.synth {
	case synthParseComplete:
		return []pgproto3.BackendMessage{&pgproto3.ParseComplete{}}
	case synthCloseComplete:
		s.forget(e.client, e.prep)
		return []pgproto3.BackendMessage{&pgproto3.CloseComplete{}}
	case synthDeallocate:
		if s.status == 'E' {
			return []pgproto3.BackendMessage{softError("25P02", msgAborted), &pgproto3.ReadyForQuery{TxStatus: 'E'}}
		}
		tag := "DEALLOCATE"
		if e.client == "*" {
			tag = "DEALLOCATE ALL"
			s.dropStatements(e.upTo)
		} else {
			s.forget(e.client, e.prep)
		}
		return []pgproto3.BackendMessage{&pgproto3.CommandComplete{CommandTag: []byte(tag)}, &pgproto3.ReadyForQuery{TxStatus: s.status}}
	}
	return nil
}

// forget removes a registry entry, unless the client has prepared that name
// again since.
func (s *session) forget(name string, p *prepared) {
	if p != nil && s.stmts[name] == p {
		s.unregister(name)
	}
}

// undo reverses what a failed or skipped entry assumed would happen.
func (s *session) undo(l *Lease, e entry) {
	if e.kind == entryClose && e.stmt != "" {
		// An eviction that never ran: the connection still holds the
		// statement, as its least recently used.
		l.Conn().Statements[e.stmt] = true
		return
	}
	if e.kind == entryParse && e.stmt != "" {
		l.Conn().dropStatement(e.stmt)
	}
	if e.unnamed {
		s.unnamedOnLease = false
		if e.prep != nil && s.unnamed == e.prep {
			s.unnamed = nil
		}
		return
	}
	if e.kind == entryParse || e.synth == synthParseComplete {
		s.forget(e.client, e.prep)
	}
}

// dropStatements forgets the client's statements registered up to upTo.
func (s *session) dropStatements(upTo uint64) {
	for name, p := range s.stmts {
		if p.seq <= upTo {
			s.unregister(name)
		}
	}
}

// resetBackendStatements records that the backend dropped its prepared
// statements, keeping those that Parses still in flight will create.
func (s *session) resetBackendStatements(c *BackendConn) {
	c.resetStatements()
	for _, e := range s.seq.q {
		if e.kind == entryParse && e.stmt != "" {
			c.addStatement(e.stmt)
		}
	}
}

// parsePending reports whether a Parse creating the backend statement name is
// still in flight.
func (s *session) parsePending(name string) bool {
	for _, e := range s.seq.q {
		if e.kind == entryParse && e.stmt == name {
			return true
		}
	}
	return false
}

// tracked reports whether a statement's effect has to follow it to Execute.
func tracked(c Classification) bool {
	switch c.Effect {
	case EffectSet, EffectReset, EffectDiscardAll, EffectDeallocate, EffectUnlisten:
		return true
	}
	return false
}

// A backend connection outlives every client that prepares statements on it,
// so its statements are capped. When a new one would take it past the cap,
// the session closes the least recently used ones first, sending the backend
// a Close ahead of the Parse. A client that needs an evicted statement again
// gets it prepared again, as on any connection that lacks it. Statements the
// current lease has used are never evicted, since a portal bound from one
// would close with it: a transaction that needs more than the cap keeps them
// all, and the connection sheds the excess once it ends.

// hasStatement reports whether the connection holds the backend statement,
// counting a yes as a use.
func (c *BackendConn) hasStatement(name string) bool {
	if !c.Statements[name] {
		return false
	}
	c.touch(name)
	return true
}

// addStatement records a statement the connection is about to hold.
func (c *BackendConn) addStatement(name string) {
	if c.Statements == nil {
		c.Statements = map[string]bool{}
	}
	c.Statements[name] = true
	c.touch(name)
}

func (c *BackendConn) touch(name string) {
	if c.used == nil {
		c.used = map[string]uint64{}
	}
	c.clock++
	c.used[name] = c.clock
}

// dropStatement forgets one statement.
func (c *BackendConn) dropStatement(name string) {
	delete(c.Statements, name)
	delete(c.used, name)
}

// resetStatements forgets every statement, as after DISCARD ALL.
func (c *BackendConn) resetStatements() {
	c.Statements = map[string]bool{}
	c.used = nil
}

// markLease starts a new lease: statements it uses from here on are kept.
func (c *BackendConn) markLease() { c.leaseMark = c.clock }

// victims returns the statements to evict, least recently used first, so
// that one more fits under the cap. A statement with no recorded use counts
// as the oldest.
func (c *BackendConn) victims() []string {
	extra := len(c.Statements) + 1 - c.maxStatements
	if c.maxStatements <= 0 || extra <= 0 {
		return nil
	}
	var old []string
	for name := range c.Statements {
		if c.used[name] <= c.leaseMark {
			old = append(old, name)
		}
	}
	slices.SortFunc(old, func(a, b string) int { return cmp.Compare(c.used[a], c.used[b]) })
	return old[:min(extra, len(old))]
}
