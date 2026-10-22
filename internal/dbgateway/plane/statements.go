package plane

import (
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
	if pins(class) {
		s.pinned = true
	}
	s.stmtSeq++
	p := &prepared{query: m.Query, oids: slices.Clone(m.ParameterOIDs), class: class, seq: s.stmtSeq}
	if m.Name == "" {
		s.unnamed, s.unnamedOnLease = p, true
		return s.forwardLocked(l, false, &entry{kind: entryParse, unnamed: true, prep: p}, m)
	}

	p.backend = statementName(m.Query, m.ParameterOIDs)
	s.stmts[m.Name] = p
	c := l.Conn()
	if c.Statements[p.backend] {
		s.answerLocked(entry{kind: entrySynthetic, synth: synthParseComplete, client: m.Name, prep: p})
		return false, nil
	}
	// Marked now, not at ParseComplete, so a second Parse of the same text
	// in the same pipeline doesn't send it twice. undo clears the mark if
	// the Parse fails or is skipped.
	c.Statements[p.backend] = true
	return s.forwardLocked(l, false, &entry{kind: entryParse, stmt: p.backend, client: m.Name, prep: p},
		&pgproto3.Parse{Name: p.backend, Query: m.Query, ParameterOIDs: m.ParameterOIDs})
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
	var msgs []pgproto3.FrontendMessage
	if parse := s.prepareOn(l, p); parse != nil {
		msgs = append(msgs, parse)
	}
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
	var msgs []pgproto3.FrontendMessage
	if parse := s.prepareOn(l, p); parse != nil {
		msgs = append(msgs, parse)
	}
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

// prepareOn returns the Parse to send ahead of a Bind or Describe of p when
// the leased connection lacks it, pushing its entry. Called with s.mu held.
func (s *session) prepareOn(l *Lease, p *prepared) *pgproto3.Parse {
	if p == nil {
		return nil
	}
	if p.backend == "" {
		if s.unnamedOnLease {
			return nil
		}
		s.unnamedOnLease = true
		s.seq.push(entry{kind: entryParse, injected: true, unnamed: true})
		return &pgproto3.Parse{Query: p.query, ParameterOIDs: p.oids}
	}
	c := l.Conn()
	if c.Statements[p.backend] {
		return nil
	}
	c.Statements[p.backend] = true
	s.seq.push(entry{kind: entryParse, injected: true, stmt: p.backend})
	return &pgproto3.Parse{Name: p.backend, Query: p.query, ParameterOIDs: p.oids}
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
		delete(s.stmts, name)
	}
}

// undo reverses what a failed or skipped entry assumed would happen.
func (s *session) undo(l *Lease, e entry) {
	if e.kind == entryParse && e.stmt != "" {
		delete(l.Conn().Statements, e.stmt)
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
			delete(s.stmts, name)
		}
	}
}

// resetBackendStatements records that the backend dropped its prepared
// statements, keeping those that Parses still in flight will create.
func (s *session) resetBackendStatements(c *BackendConn) {
	c.Statements = map[string]bool{}
	for _, e := range s.seq.q {
		if e.kind == entryParse && e.stmt != "" {
			c.Statements[e.stmt] = true
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
