package plane

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"
)

// capStatements sets the pool's per-connection cap before anything dials.
func capStatements(h *harness, n int) { h.pool.cfg.MaxStatements = n }

// capRegistry sets the client registry's caps for one test.
func capRegistry(t *testing.T, n, bytes int) {
	t.Helper()
	oldN, oldBytes := maxClientStatements, maxClientStatementBytes
	maxClientStatements, maxClientStatementBytes = n, bytes
	t.Cleanup(func() { maxClientStatements, maxClientStatementBytes = oldN, oldBytes })
}

// prepare parses one named statement and syncs.
func (c *testClient) prepare(name, query string) {
	c.t.Helper()
	c.send(&pgproto3.Parse{Name: name, Query: query}, &pgproto3.Sync{})
	want(c.t, "prepare "+name, c.read(2), []string{"ParseComplete", "ReadyForQuery I"})
}

// run binds and executes a statement of the form SELECT x and checks its row.
func (c *testClient) run(name, row string) {
	c.t.Helper()
	c.send(&pgproto3.Bind{PreparedStatement: name}, &pgproto3.Execute{}, &pgproto3.Sync{})
	want(c.t, "run "+name, c.read(4), []string{"BindComplete", "DataRow " + row, "CommandComplete SELECT 1", "ReadyForQuery I"})
}

// liveStatements is how many lp_ statements a fake backend holds, worked
// out from the Parses and Closes it read.
func liveStatements(f *fakePG) int {
	live := map[string]bool{}
	for _, m := range f.received() {
		if rest, ok := strings.CutPrefix(m, "Parse lp_"); ok {
			live[strings.Fields(rest)[0]] = true
		}
		if rest, ok := strings.CutPrefix(m, "Close S lp_"); ok {
			delete(live, rest)
		}
	}
	return len(live)
}

func TestSessionEvictsLeastRecentlyUsedStatement(t *testing.T) {
	h := newHarness(t, 1, time.Second)
	capStatements(h, 2)
	c := h.connect(nil)
	c.prepare("a", "SELECT 1")
	c.prepare("b", "SELECT 2")
	c.run("a", "1") // b is now the least recently used

	c.prepare("c", "SELECT 3")
	a, b, cc := statementName("SELECT 1", nil), statementName("SELECT 2", nil), statementName("SELECT 3", nil)
	got := h.dialer.server(0).received()
	want(t, "eviction", got[len(got)-3:], []string{"Close S " + b, "Parse " + cc + " SELECT 3", "Sync"})

	// The evicted statement is prepared again when the client uses it, and
	// the least recently used one goes to make room.
	c.run("b", "2")
	got = h.dialer.server(0).received()
	want(t, "re-prepare", got[len(got)-5:], []string{"Close S " + a, "Parse " + b + " SELECT 2", "Bind " + b, "Execute", "Sync"})
	c.run("c", "3")
	c.run("a", "1")
	if n := liveStatements(h.dialer.server(0)); n != 2 {
		t.Fatalf("backend holds %d statements, want the cap of 2", n)
	}
	c.terminate()
}

func TestSessionKeepsStatementsItsTransactionUses(t *testing.T) {
	h := newHarness(t, 1, time.Second)
	capStatements(h, 2)
	c := h.connect(nil)
	c.prepare("old", "SELECT 9")

	c.query("BEGIN")
	// Bind a portal from a, then prepare past the cap: closing a would take
	// the portal with it.
	c.send(&pgproto3.Parse{Name: "a", Query: "SELECT 1"}, &pgproto3.Bind{PreparedStatement: "a", DestinationPortal: "p"},
		&pgproto3.Parse{Name: "b", Query: "SELECT 2"}, &pgproto3.Parse{Name: "c", Query: "SELECT 3"},
		&pgproto3.Execute{Portal: "p"}, &pgproto3.Sync{})
	want(t, "transaction", c.read(7), []string{
		"ParseComplete", "BindComplete", "ParseComplete", "ParseComplete", "DataRow 1", "CommandComplete SELECT 1", "ReadyForQuery T",
	})
	f := h.dialer.server(0)
	old := statementName("SELECT 9", nil)
	for _, m := range f.received() {
		if strings.HasPrefix(m, "Close S") && m != "Close S "+old {
			t.Fatalf("backend got %q inside the transaction, want only statements from earlier leases closed", m)
		}
	}
	want(t, "commit", c.query("COMMIT"), []string{"CommandComplete COMMIT", "ReadyForQuery I"})

	// Once the transaction is over the connection sheds what it holds past
	// the cap.
	c.prepare("d", "SELECT 4")
	if n := liveStatements(f); n != 2 {
		t.Fatalf("backend holds %d statements after the transaction, want 2", n)
	}
	for name, row := range map[string]string{"a": "1", "b": "2", "c": "3", "d": "4", "old": "9"} {
		c.run(name, row)
	}
	c.terminate()
}

func TestSessionSkippedEvictionKeepsStatement(t *testing.T) {
	h := newHarness(t, 1, time.Second)
	capStatements(h, 2)
	c := h.connect(nil)
	c.prepare("a", "SELECT 1")
	c.prepare("b", "SELECT 2")

	// The Bind fails, so the backend skips the eviction's Close and the
	// Parse behind it.
	c.send(&pgproto3.Bind{PreparedStatement: "nope"}, &pgproto3.Parse{Name: "c", Query: "SELECT 3"}, &pgproto3.Sync{})
	got := c.read(2)
	if !strings.HasPrefix(got[0], "ErrorResponse ERROR 26000") || got[1] != "ReadyForQuery I" {
		t.Fatalf("pipeline = %q, want 26000", got)
	}
	// a is still on the connection: a second Parse of it would fail 42P05.
	c.run("a", "1")
	c.run("b", "2")
	c.send(&pgproto3.Bind{PreparedStatement: "c"}, &pgproto3.Sync{})
	if got := c.read(2); !strings.HasPrefix(got[0], "ErrorResponse ERROR 26000") {
		t.Fatalf("Bind of the skipped statement = %q, want 26000", got)
	}
	c.terminate()
}

func TestSessionStatementRegistryCap(t *testing.T) {
	h := newHarness(t, 1, time.Second)
	capRegistry(t, 2, 1<<20)
	c := h.connect(nil)
	c.prepare("a", "SELECT 1")
	c.prepare("b", "SELECT 2")
	// Preparing a name again replaces it, so it fits.
	c.prepare("a", "SELECT 3")

	c.send(&pgproto3.Parse{Name: "c", Query: "SELECT 4"}, &pgproto3.Bind{PreparedStatement: "a"}, &pgproto3.Execute{}, &pgproto3.Sync{})
	got := c.read(2)
	if got[0] != "ErrorResponse ERROR 54000 "+msgTooManyStatements || got[1] != "ReadyForQuery I" {
		t.Fatalf("Parse past the cap = %q, want 54000 and the rest skipped", got)
	}

	// Inside a transaction the refusal aborts it, as an error would.
	c.query("BEGIN")
	c.send(&pgproto3.Parse{Name: "c", Query: "SELECT 4"}, &pgproto3.Sync{})
	if got := c.read(2); !strings.HasPrefix(got[0], "ErrorResponse ERROR 54000") || got[1] != "ReadyForQuery E" {
		t.Fatalf("Parse past the cap in a transaction = %q, want 54000 and status E", got)
	}
	c.send(&pgproto3.Parse{Name: "c", Query: "SELECT 4"}, &pgproto3.Sync{})
	if got := c.read(2); !strings.HasPrefix(got[0], "ErrorResponse ERROR 25P02") {
		t.Fatalf("Parse in the aborted transaction = %q, want the backend's 25P02", got)
	}
	c.query("ROLLBACK")

	// Closing one makes room.
	c.send(&pgproto3.Close{ObjectType: 'S', Name: "b"}, &pgproto3.Sync{})
	want(t, "close", c.read(2), []string{"CloseComplete", "ReadyForQuery I"})
	c.prepare("c", "SELECT 4")
	c.run("c", "4")
	c.run("a", "3")
	c.terminate()
}

func TestSessionStatementRegistryByteCap(t *testing.T) {
	h := newHarness(t, 1, time.Second)
	capRegistry(t, 100, len("SELECT 1")*2)
	c := h.connect(nil)
	c.prepare("a", "SELECT 1")
	c.prepare("b", "SELECT 2")
	c.send(&pgproto3.Parse{Name: "c", Query: "SELECT 3"}, &pgproto3.Sync{})
	if got := c.read(2); !strings.HasPrefix(got[0], "ErrorResponse ERROR 54000") {
		t.Fatalf("Parse past the byte cap = %q, want 54000", got)
	}
	// The unnamed statement isn't in the registry.
	c.send(&pgproto3.Parse{Query: "SELECT 3"}, &pgproto3.Bind{}, &pgproto3.Execute{}, &pgproto3.Sync{})
	want(t, "unnamed", c.read(5), []string{"ParseComplete", "BindComplete", "DataRow 3", "CommandComplete SELECT 1", "ReadyForQuery I"})
	c.terminate()
}

// TestSessionManyStatementsStayWithinCaps is a long-running client that
// prepares thousands of distinct named statements, keeping the most recent
// ones open as a driver's statement cache would, and reuses older ones.
func TestSessionManyStatementsStayWithinCaps(t *testing.T) {
	const (
		total     = 3000
		connCap   = 8
		clientCap = 16
	)
	h := newHarness(t, 2, time.Second)
	capStatements(h, connCap)
	capRegistry(t, clientCap, 1<<20)
	c := h.connect(nil)
	for i := range total {
		name := fmt.Sprintf("s%d", i)
		c.send(&pgproto3.Parse{Name: name, Query: fmt.Sprintf("SELECT %d", i)},
			&pgproto3.Bind{PreparedStatement: name}, &pgproto3.Execute{}, &pgproto3.Sync{})
		want(t, name, c.read(5), []string{"ParseComplete", "BindComplete", fmt.Sprintf("DataRow %d", i), "CommandComplete SELECT 1", "ReadyForQuery I"})
		// An older statement, long since evicted from the connection.
		if i >= clientCap-1 {
			old := i - (clientCap - 2)
			c.run(fmt.Sprintf("s%d", old), fmt.Sprint(old))
		}
		// The driver's cache closes what falls out of it.
		if i >= clientCap-1 {
			c.send(&pgproto3.Close{ObjectType: 'S', Name: fmt.Sprintf("s%d", i-(clientCap-1))}, &pgproto3.Sync{})
			want(t, "close", c.read(2), []string{"CloseComplete", "ReadyForQuery I"})
		}
	}
	for i := range h.dialer.dials() {
		if n := liveStatements(h.dialer.server(i)); n > connCap {
			t.Fatalf("backend %d holds %d statements, want at most %d", i, n, connCap)
		}
	}
	// One more fills the registry, and the next is refused.
	c.prepare("fill", "SELECT 1")
	c.send(&pgproto3.Parse{Name: "extra", Query: "SELECT 1"}, &pgproto3.Sync{})
	if got := c.read(2); !strings.HasPrefix(got[0], "ErrorResponse ERROR 54000") {
		t.Fatalf("Parse with the registry full = %q, want 54000", got)
	}
	c.terminate()
}
