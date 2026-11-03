package agentchat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/xraph/cortex/id"
	"github.com/xraph/warden"
	wardenmemory "github.com/xraph/warden/store/memory"

	"github.com/xraph/loomplane/internal/agentruns"
	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/security"
)

// Workspaces: orgA, where Ada and Bo develop and Al administers; orgB,
// where Bob owns.
const (
	orgA = "o"
	orgB = "o2"
	ada  = "usr_ada"
	bo   = "usr_bo"
	al   = "usr_al"
	bob  = "usr_bob"
)

var members = map[string]map[string]string{
	orgA: {ada: security.MemberMember, bo: security.MemberMember, al: security.MemberAdmin},
	orgB: {bob: security.MemberOwner},
}

func person(org, user string) security.Principal {
	return security.Principal{Kind: security.KindUser, UserID: user, OrgID: org}
}

// newAuthz is the real Authorizer over an in-memory Warden.
func newAuthz(t *testing.T) *security.Authorizer {
	t.Helper()
	store := wardenmemory.New()
	eng, err := warden.NewEngine(warden.WithStore(store))
	if err != nil {
		t.Fatal(err)
	}
	lookup := func(_ context.Context, org, user string) (string, bool, error) {
		r, ok := members[org][user]
		return r, ok, nil
	}
	a := security.NewAuthorizer(eng, store, lookup)
	for org, people := range members {
		if err := a.AssignAgent(t.Context(), org); err != nil {
			t.Fatal(err)
		}
		for user, role := range people {
			if err := a.Assign(t.Context(), org, user, security.RoleForMember(role), "test"); err != nil {
				t.Fatal(err)
			}
		}
	}
	return a
}

// row is a chat task as the fake store keeps it.
type row struct {
	task  agentruns.Task
	input string
}

// memStore is the Store over a slice of chat tasks, worked out the way
// PgStore's SQL does.
type memStore struct {
	mu   sync.Mutex
	rows []row
}

func (m *memStore) add(t agentruns.Task, input string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows = append(m.rows, row{task: t, input: input})
}

func (m *memStore) setStatus(taskID, status string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.rows {
		if m.rows[i].task.ID == taskID {
			m.rows[i].task.Status = status
		}
	}
}

// session is a conversation's tasks, oldest first.
func (m *memStore) session(orgID, id string) []row {
	var out []row
	for _, r := range m.rows {
		if r.task.OrgID == orgID && r.task.SessionID == id && r.task.Kind == agentruns.KindChat {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].task.CreatedAt.Before(out[j].task.CreatedAt) })
	return out
}

func fold(rows []row) Conversation {
	first, last := rows[0].task, rows[len(rows)-1].task
	return Conversation{
		ID: first.SessionID, OrgID: first.OrgID, ProjectID: first.ProjectID, Title: titleOf(rows[0].input),
		StartedBy: first.RequestedBy, Status: last.Status, LastTaskID: last.ID, Turns: len(rows),
		CreatedAt: first.CreatedAt, UpdatedAt: last.UpdatedAt,
	}
}

func (m *memStore) Conversation(_ context.Context, orgID, id string) (*Conversation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rows := m.session(orgID, id)
	if len(rows) == 0 {
		return nil, ErrNotFound
	}
	c := fold(rows)
	return &c, nil
}

func (m *memStore) List(_ context.Context, orgID, startedBy string, f ListFilter) ([]Conversation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	seen := map[string]bool{}
	var out []Conversation
	for _, r := range m.rows {
		if r.task.OrgID != orgID || seen[r.task.SessionID] {
			continue
		}
		seen[r.task.SessionID] = true
		c := fold(m.session(orgID, r.task.SessionID))
		if (startedBy != "" && c.StartedBy != startedBy) || (f.ProjectID != "" && c.ProjectID != f.ProjectID) ||
			(!f.Before.IsZero() && !c.UpdatedAt.Before(f.Before)) {
			continue
		}
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	if len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}

func (m *memStore) Turns(_ context.Context, orgID, id, before string, limit int) ([]TurnRow, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rows := m.session(orgID, id)
	if before != "" {
		i := slices.IndexFunc(rows, func(r row) bool { return r.task.ID == before })
		if i < 0 {
			return []TurnRow{}, false, nil
		}
		rows = rows[:i]
	}
	out := []TurnRow{}
	for i := len(rows) - 1; i >= 0 && len(out) < limit; i-- {
		t := rows[i].task
		out = append(out, TurnRow{TaskID: t.ID, Input: rows[i].input, Status: t.Status, Error: t.Error,
			EventSeq: t.EventSeq, CreatedAt: t.CreatedAt, FinishedAt: t.FinishedAt})
	}
	return out, len(rows) > limit, nil
}

// eventsCall is one Runs.Events call.
type eventsCall struct {
	principal security.Principal
	taskID    string
	after     int64
	limit     int
}

// fakeRuns is agentruns as the service uses it. Start records what it was
// asked and adds the task to the store with status startAs.
type fakeRuns struct {
	mu      sync.Mutex
	store   *memStore
	clock   *clock
	starts  []agentruns.StartInput
	err     error
	startAs string
	delay   time.Duration
	events  map[string][]agentruns.Event
	reads   []eventsCall
	next    int
}

func (f *fakeRuns) Start(_ context.Context, in agentruns.StartInput) (*agentruns.Task, error) {
	f.mu.Lock()
	f.starts = append(f.starts, in)
	err := f.err
	f.next++
	n := f.next
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	session := in.SessionID
	if session == "" {
		session = id.NewSessionID().String()
	}
	now := f.clock.tick()
	status := f.startAs
	if status == "" {
		status = agentruns.StatusQueued
	}
	t := agentruns.Task{
		ID: fmt.Sprintf("atask_%03d", n), OrgID: in.OrgID, ProjectID: in.ProjectID, Kind: in.Kind, SessionID: session,
		Status: status, RequestedBy: in.OnBehalfOf.UserID, Metadata: in.Metadata, CreatedAt: now, UpdatedAt: now,
	}
	f.store.add(t, in.Input)
	return &t, nil
}

func (f *fakeRuns) Events(_ context.Context, p security.Principal, taskID string, after int64, limit int) ([]agentruns.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if limit < 1 || limit > 1000 {
		return nil, fmt.Errorf("%w: read 1 to 1000 events", agentruns.ErrInvalid)
	}
	f.reads = append(f.reads, eventsCall{principal: p, taskID: taskID, after: after, limit: limit})
	out := []agentruns.Event{}
	for _, e := range f.events[taskID] {
		if e.Seq > after && len(out) < limit {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *fakeRuns) started() []agentruns.StartInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.starts)
}

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) tick() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(time.Second)
	return c.now
}

// memLock serialises each name in memory, as pglock.Do does across
// replicas.
type memLock struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
	names []string
}

func (l *memLock) Do(ctx context.Context, name string, fn func(context.Context) error) error {
	l.mu.Lock()
	if l.locks == nil {
		l.locks = map[string]*sync.Mutex{}
	}
	m, ok := l.locks[name]
	if !ok {
		m = &sync.Mutex{}
		l.locks[name] = m
	}
	l.names = append(l.names, name)
	l.mu.Unlock()
	m.Lock()
	defer m.Unlock()
	return fn(ctx)
}

type world struct {
	svc   *Service
	store *memStore
	runs  *fakeRuns
	lock  *memLock
	clock *clock
}

func newWorld(t *testing.T) *world {
	t.Helper()
	st := &memStore{}
	c := &clock{now: time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)}
	runs := &fakeRuns{store: st, clock: c, events: map[string][]agentruns.Event{}}
	lock := &memLock{}
	svc := NewService(Deps{Store: st, Runs: runs, Authz: newAuthz(t), Lock: lock.Do, Now: func() time.Time { return c.now }})
	return &world{svc: svc, store: st, runs: runs, lock: lock, clock: c}
}

// open starts a conversation as user in orgA and marks its first turn
// done, so the next message may follow.
func (w *world) open(t *testing.T, user, message, project string) Conversation {
	t.Helper()
	sent, err := w.svc.Start(t.Context(), person(orgA, user), StartInput{Message: message, ProjectID: project})
	if err != nil {
		t.Fatal(err)
	}
	w.store.setStatus(sent.Task.ID, agentruns.StatusCompleted)
	return sent.Conversation
}

func TestStartOpensAConversation(t *testing.T) {
	w := newWorld(t)
	sent, err := w.svc.Start(t.Context(), person(orgA, ada), StartInput{Message: "  What's deployed?  ", ProjectID: "proj_1"})
	if err != nil {
		t.Fatal(err)
	}
	starts := w.runs.started()
	if len(starts) != 1 {
		t.Fatalf("started %d tasks", len(starts))
	}
	in := starts[0]
	if in.Kind != agentruns.KindChat || in.OrgID != orgA || in.OnBehalfOf == nil || in.OnBehalfOf.UserID != ada ||
		in.ProjectID != "proj_1" || in.Input != "What's deployed?" || in.Metadata[agentruns.MetaTitle] != "What's deployed?" {
		t.Fatalf("start input %+v", in)
	}
	if in.SessionID != "" {
		t.Fatalf("a new conversation passed session %q; agentruns makes it", in.SessionID)
	}
	if _, perr := id.ParseSessionID(sent.Task.SessionID); perr != nil {
		t.Fatalf("session %q: %v", sent.Task.SessionID, perr)
	}
	c := sent.Conversation
	if c.ID != sent.Task.SessionID || c.StartedBy != ada || c.Status != agentruns.StatusQueued || c.OrgID != orgA ||
		c.ProjectID != "proj_1" || c.Title != "What's deployed?" || c.LastTaskID != sent.Task.ID || c.Turns != 1 {
		t.Fatalf("conversation %+v", c)
	}
}

func TestTitleIsTheFirstMessageCut(t *testing.T) {
	w := newWorld(t)
	words := strings.Repeat("deploy the api again ", 10) // 210 characters
	sent, err := w.svc.Start(t.Context(), person(orgA, ada), StartInput{Message: words})
	if err != nil {
		t.Fatal(err)
	}
	title := sent.Conversation.Title
	if utf8.RuneCountInString(title) > TitleLen || title == "" {
		t.Fatalf("title %q is %d runes", title, utf8.RuneCountInString(title))
	}
	if strings.HasSuffix(title, " ") || !strings.HasPrefix(strings.TrimSpace(words), title) {
		t.Fatalf("title %q", title)
	}
	if next := strings.TrimSpace(words)[len(title)]; next != ' ' {
		t.Fatalf("title %q cuts a word (next %q)", title, next)
	}
	if got := w.runs.started()[0].Metadata[agentruns.MetaTitle]; got != title {
		t.Fatalf("metadata title %q, conversation %q", got, title)
	}

	// A word longer than the title is cut at TitleLen.
	if got := titleOf(strings.Repeat("é", 200)); utf8.RuneCountInString(got) != TitleLen {
		t.Fatalf("one long word: %d runes", utf8.RuneCountInString(got))
	}
	// Lines and runs of spaces read as one space.
	if got := titleOf("Why is\n\nthe   api down?"); got != "Why is the api down?" {
		t.Fatalf("title %q", got)
	}
}

func TestMessageLength(t *testing.T) {
	w := newWorld(t)
	c := w.open(t, ada, "first", "")
	for _, msg := range []string{"", "   ", strings.Repeat("a", MaxMessage+1)} {
		if _, err := w.svc.Start(t.Context(), person(orgA, ada), StartInput{Message: msg}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Start(%d chars): %v", len(msg), err)
		}
		if _, err := w.svc.Send(t.Context(), person(orgA, ada), c.ID, SendInput{Message: msg}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Send(%d chars): %v", len(msg), err)
		}
	}
	if n := len(w.runs.started()); n != 1 {
		t.Fatalf("started %d tasks, want only the first", n)
	}
	// MaxMessage counts characters, not bytes.
	if _, err := w.svc.Start(t.Context(), person(orgA, ada), StartInput{Message: strings.Repeat("é", MaxMessage)}); err != nil {
		t.Fatalf("a message of MaxMessage characters: %v", err)
	}
}

func TestAgentsCannotChat(t *testing.T) {
	w := newWorld(t)
	c := w.open(t, ada, "hello", "")
	agent := security.AgentPrincipal(orgA, nil)
	if _, err := w.svc.Start(t.Context(), agent, StartInput{Message: "hi"}); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("agent Start: %v", err)
	}
	onBehalf := security.AgentPrincipal(orgA, &security.Principal{Kind: security.KindUser, UserID: ada, OrgID: orgA})
	if _, err := w.svc.Send(t.Context(), onBehalf, c.ID, SendInput{Message: "hi"}); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("agent for Ada Send: %v", err)
	}
	if n := len(w.runs.started()); n != 1 {
		t.Fatalf("started %d tasks", n)
	}
}

func TestSendContinuesTheSameSession(t *testing.T) {
	w := newWorld(t)
	c := w.open(t, ada, "What's deployed?", "proj_1")
	sent, err := w.svc.Send(t.Context(), person(orgA, ada), c.ID, SendInput{Message: " And staging? "})
	if err != nil {
		t.Fatal(err)
	}
	in := w.runs.started()[1]
	if in.SessionID != c.ID || in.ProjectID != "proj_1" || in.Kind != agentruns.KindChat || in.Input != "And staging?" ||
		in.OnBehalfOf == nil || in.OnBehalfOf.UserID != ada || in.OrgID != orgA {
		t.Fatalf("send input %+v", in)
	}
	got := sent.Conversation
	if got.ID != c.ID || got.Turns != 2 || got.LastTaskID != sent.Task.ID || got.Status != agentruns.StatusQueued ||
		got.Title != "What's deployed?" || got.ProjectID != "proj_1" || !got.CreatedAt.Equal(c.CreatedAt) {
		t.Fatalf("conversation after send %+v", got)
	}
	if want := []string{"agent-conversation:" + c.ID}; !slices.Equal(w.lock.names, want) {
		t.Fatalf("locks %v, want %v", w.lock.names, want)
	}
}

func TestSendWhileBusyRefuses(t *testing.T) {
	for _, status := range []string{agentruns.StatusQueued, agentruns.StatusRunning, agentruns.StatusWaiting} {
		w := newWorld(t)
		c := w.open(t, ada, "hello", "")
		w.store.setStatus(c.LastTaskID, status)
		if _, err := w.svc.Send(t.Context(), person(orgA, ada), c.ID, SendInput{Message: "again"}); !errors.Is(err, ErrBusy) {
			t.Fatalf("%s: %v", status, err)
		}
		if n := len(w.runs.started()); n != 1 {
			t.Fatalf("%s: started %d tasks", status, n)
		}
	}
	for _, status := range []string{agentruns.StatusCompleted, agentruns.StatusFailed, agentruns.StatusCancelled} {
		w := newWorld(t)
		c := w.open(t, ada, "hello", "")
		w.store.setStatus(c.LastTaskID, status)
		if _, err := w.svc.Send(t.Context(), person(orgA, ada), c.ID, SendInput{Message: "again"}); err != nil {
			t.Fatalf("%s: %v", status, err)
		}
		if n := len(w.runs.started()); n != 2 {
			t.Fatalf("%s: started %d tasks", status, n)
		}
	}
	if ErrBusy.Error() != "The agent is still on your last message. Wait for it to finish, answer what it asked, or stop it." {
		t.Fatalf("busy text %q", ErrBusy)
	}
}

func TestConcurrentSendsStartOne(t *testing.T) {
	w := newWorld(t)
	c := w.open(t, ada, "hello", "")
	// Each Start takes a while and leaves its task running, so a second
	// send that read the status before the first's task existed would
	// start a turn too.
	w.runs.mu.Lock()
	w.runs.startAs, w.runs.delay = agentruns.StatusRunning, 50*time.Millisecond
	w.runs.mu.Unlock()

	var wg sync.WaitGroup
	results := make([]error, 2)
	sent := make([]*Sent, 2)
	for i := range 2 {
		wg.Go(func() {
			sent[i], results[i] = w.svc.Send(context.Background(), person(orgA, ada), c.ID, SendInput{Message: fmt.Sprintf("go %d", i)})
		})
	}
	wg.Wait()
	ok, busy := 0, 0
	for i, err := range results {
		switch {
		case err == nil && sent[i] != nil:
			ok++
		case errors.Is(err, ErrBusy):
			busy++
		default:
			t.Fatalf("send %d: %v", i, err)
		}
	}
	if ok != 1 || busy != 1 {
		t.Fatalf("%d sent, %d busy", ok, busy)
	}
	if n := len(w.runs.started()); n != 2 {
		t.Fatalf("started %d tasks, want the first and one send", n)
	}
}

func TestConversationsArePrivate(t *testing.T) {
	w := newWorld(t)
	c := w.open(t, ada, "my secret plans", "")

	if _, err := w.svc.History(t.Context(), person(orgA, bo), c.ID, HistoryQuery{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Bo History: %v", err)
	}
	if _, err := w.svc.Send(t.Context(), person(orgA, bo), c.ID, SendInput{Message: "hi"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Bo Send: %v", err)
	}
	h, err := w.svc.History(t.Context(), person(orgA, al), c.ID, HistoryQuery{})
	if err != nil || h.Conversation.ID != c.ID || len(h.Turns) != 1 {
		t.Fatalf("admin History %+v, %v", h, err)
	}
	if _, err = w.svc.Send(t.Context(), person(orgA, al), c.ID, SendInput{Message: "hi"}); !errors.Is(err, ErrNotYours) {
		t.Fatalf("admin Send: %v", err)
	}
	if _, err = w.svc.History(t.Context(), person(orgB, bob), c.ID, HistoryQuery{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other org History: %v", err)
	}
	if _, err = w.svc.Send(t.Context(), person(orgB, bob), c.ID, SendInput{Message: "hi"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other org Send: %v", err)
	}
	if _, err = w.svc.History(t.Context(), person(orgA, ada), "sess_nope", HistoryQuery{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown History: %v", err)
	}
	if n := len(w.runs.started()); n != 1 {
		t.Fatalf("started %d tasks", n)
	}
}

func TestListShowsOnlyYours(t *testing.T) {
	w := newWorld(t)
	mine := w.open(t, bo, "Bo's question", "")
	theirs := w.open(t, ada, "Ada's question", "")

	list, err := w.svc.List(t.Context(), person(orgA, bo), ListFilter{})
	if err != nil || len(list.Conversations) != 1 || list.Conversations[0].ID != mine.ID {
		t.Fatalf("Bo's list %+v, %v", list, err)
	}
	if _, err = w.svc.List(t.Context(), person(orgA, bo), ListFilter{Everyone: true}); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("Everyone as developer: %v", err)
	}
	all, err := w.svc.List(t.Context(), person(orgA, al), ListFilter{Everyone: true})
	if err != nil || len(all.Conversations) != 2 || all.Conversations[0].ID != theirs.ID || all.Conversations[1].ID != mine.ID {
		t.Fatalf("admin's list %+v, %v", all, err)
	}
	own, err := w.svc.List(t.Context(), person(orgA, al), ListFilter{})
	if err != nil || len(own.Conversations) != 0 || own.Conversations == nil {
		t.Fatalf("admin's own list %+v, %v", own, err)
	}
	other, err := w.svc.List(t.Context(), person(orgB, bob), ListFilter{Everyone: true})
	if err != nil || len(other.Conversations) != 0 {
		t.Fatalf("other org's list %+v, %v", other, err)
	}
}

func TestHistoryPagesTurns(t *testing.T) {
	w := newWorld(t)
	c := w.open(t, ada, "turn 1", "")
	for i := 2; i <= 25; i++ {
		sent, err := w.svc.Send(t.Context(), person(orgA, ada), c.ID, SendInput{Message: fmt.Sprintf("turn %d", i)})
		if err != nil {
			t.Fatal(err)
		}
		w.store.setStatus(sent.Task.ID, agentruns.StatusCompleted)
	}
	// Each turn's log; the newest turn's runs past one page of events and
	// ends with a safety stop that names its step.
	rows := w.store.session(orgA, c.ID)
	tasks := make([]string, 0, len(rows))
	for _, r := range rows {
		tasks = append(tasks, r.task.ID)
	}
	for i, taskID := range tasks {
		n := 3
		if i == len(tasks)-1 {
			n = 1500
		}
		evs := make([]agentruns.Event, 0, n)
		for seq := int64(1); seq <= int64(n); seq++ {
			evs = append(evs, agentruns.Event{TaskID: taskID, Seq: seq, Kind: "token", Data: json.RawMessage(`{"text":"x"}`)})
		}
		if i == len(tasks)-1 {
			evs[n-1] = agentruns.Event{TaskID: taskID, Seq: int64(n), Kind: "safety_block",
				Data: json.RawMessage(`{"message":"Loomplane's safety checks stopped this answer.","step_seq":1400}`)}
		}
		w.runs.events[taskID] = evs
	}

	h, err := w.svc.History(t.Context(), person(orgA, ada), c.ID, HistoryQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Turns) != 20 || !h.More || h.Turns[0].Message != "turn 6" || h.Turns[19].Message != "turn 25" {
		t.Fatalf("first page: %d turns, more %v, %q..%q", len(h.Turns), h.More, h.Turns[0].Message, h.Turns[len(h.Turns)-1].Message)
	}
	if h.Conversation.ID != c.ID || h.Conversation.Turns != 25 {
		t.Fatalf("conversation %+v", h.Conversation)
	}
	for i, turn := range h.Turns[:19] {
		if turn.TaskID != tasks[i+5] || len(turn.Events) != 3 || turn.EventSeq != 3 || turn.Status != agentruns.StatusCompleted {
			t.Fatalf("turn %d: %+v", i, turn)
		}
	}
	newest := h.Turns[19]
	if len(newest.Events) != 1500 || newest.EventSeq != 1500 {
		t.Fatalf("newest turn: %d events, seq %d", len(newest.Events), newest.EventSeq)
	}
	if last := newest.Events[1499]; last.Kind != "safety_block" || !strings.Contains(string(last.Data), `"step_seq":1400`) {
		t.Fatalf("safety stop %+v", last)
	}
	for _, r := range w.runs.reads {
		if r.limit > 1000 || r.principal.UserID != ada {
			t.Fatalf("events read %+v", r)
		}
		if second := r.taskID == newest.TaskID && r.after == 1000; r.after != 0 && !second {
			t.Fatalf("events read %+v", r)
		}
	}

	older, err := w.svc.History(t.Context(), person(orgA, ada), c.ID, HistoryQuery{Before: h.Turns[0].TaskID})
	if err != nil {
		t.Fatal(err)
	}
	if len(older.Turns) != 5 || older.More || older.Turns[0].Message != "turn 1" || older.Turns[4].Message != "turn 5" {
		t.Fatalf("older page: %d turns, more %v", len(older.Turns), older.More)
	}

	few, err := w.svc.History(t.Context(), person(orgA, ada), c.ID, HistoryQuery{Limit: 2})
	if err != nil || len(few.Turns) != 2 || !few.More || few.Turns[1].Message != "turn 25" {
		t.Fatalf("limit 2: %+v, %v", few, err)
	}
}

func TestStartPassesRunErrorsThrough(t *testing.T) {
	for _, want := range []error{agentsettings.ErrBudgetExhausted, agentsettings.ErrDisabled} {
		w := newWorld(t)
		w.runs.err = fmt.Errorf("refused: %w", want)
		if _, err := w.svc.Start(t.Context(), person(orgA, ada), StartInput{Message: "hi"}); !errors.Is(err, want) {
			t.Fatalf("Start: %v, want %v", err, want)
		}
	}
	w := newWorld(t)
	c := w.open(t, ada, "hello", "")
	w.runs.err = agentsettings.ErrBudgetExhausted
	if _, err := w.svc.Send(t.Context(), person(orgA, ada), c.ID, SendInput{Message: "more"}); !errors.Is(err, agentsettings.ErrBudgetExhausted) {
		t.Fatalf("Send: %v", err)
	}
}
