package agentchat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/xraph/loomplane/internal/agentruns"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

// Turn implements Store the way PgStore's SQL does: the task must be one
// of the conversation's chat tasks, and a deleted conversation has none.
func (m *memStore) Turn(_ context.Context, orgID, id, taskID string) (*TurnRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.deleted(orgID, id) {
		return nil, ErrNotFound
	}
	for _, r := range m.session(orgID, id) {
		if r.task.ID == taskID {
			t := r.task
			return &TurnRow{TaskID: t.ID, Input: r.input, Status: t.Status, Error: t.Error, EventSeq: t.EventSeq,
				CreatedAt: t.CreatedAt, FinishedAt: t.FinishedAt, Context: r.context}, nil
		}
	}
	return nil, ErrNotFound
}

// SaveFlag implements Store: one flag per turn per person, and flagging
// again changes its reason and note and opens it again.
func (m *memStore) SaveFlag(_ context.Context, f Flag) (*Flag, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.flags {
		old := &m.flags[i]
		if old.TaskID == f.TaskID && old.ReportedBy == f.ReportedBy {
			old.Reason, old.Note, old.UpdatedAt = f.Reason, f.Note, f.UpdatedAt
			old.DismissedBy, old.DismissedAt = "", nil
			out := *old
			return &out, nil
		}
	}
	m.flags = append(m.flags, f)
	return &f, nil
}

func (m *memStore) Flags(_ context.Context, status string, limit int) ([]Flag, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Flag{}
	for _, f := range m.flags {
		dismissed := f.DismissedAt != nil
		if (status == FlagsOpen && dismissed) || (status == FlagsDismissed && !dismissed) {
			continue
		}
		out = append(out, f)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID > out[j].ID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *memStore) DismissFlag(_ context.Context, id, by string, at time.Time) (*Flag, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.flags {
		f := &m.flags[i]
		if f.ID == id {
			if f.DismissedAt == nil {
				f.DismissedBy, f.DismissedAt, f.UpdatedAt = by, &at, at
			}
			out := *f
			return &out, nil
		}
	}
	return nil, ErrFlagNotFound
}

// auditLog is the audit trail in memory.
type auditLog struct {
	mu     sync.Mutex
	events []projects.AuditEvent
}

func (a *auditLog) Record(_ context.Context, e projects.AuditEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, e)
}

func (a *auditLog) recorded() []projects.AuditEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.events)
}

// token is a token event of the agent's answer.
func token(seq int64, text string) agentruns.Event {
	data, _ := json.Marshal(map[string]any{"text": text})
	return agentruns.Event{Seq: seq, Kind: "token", Data: data}
}

// answered gives the conversation's last turn an answer of tokens.
func (w *world) answered(taskID string, texts ...string) {
	evs := make([]agentruns.Event, 0, len(texts))
	for i, text := range texts {
		evs = append(evs, token(int64(i+1), text))
	}
	w.runs.events[taskID] = evs
}

func TestFlagByTheStarter(t *testing.T) {
	w := newWorld(t)
	c := w.open(t, ada, "Why did the deploy fail?", "proj_1")
	w.answered(c.LastTaskID, "The build ", "ran out of memory.")

	f, err := w.svc.Flag(t.Context(), person(orgA, ada), c.ID, c.LastTaskID,
		FlagInput{Reason: FlagInaccurate, Note: "  It was a timeout, not memory.  "})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(f.ID, "aflag_") || f.OrgID != orgA || f.ProjectID != "proj_1" || f.ConversationID != c.ID ||
		f.TaskID != c.LastTaskID || f.ReportedBy != ada || f.Reason != "inaccurate" || f.Note != "It was a timeout, not memory." ||
		f.Question != "Why did the deploy fail?" || f.Answer != "The build ran out of memory." {
		t.Fatalf("flag %+v", f)
	}
	if f.CreatedAt.IsZero() || !f.UpdatedAt.Equal(f.CreatedAt) || f.DismissedBy != "" || f.DismissedAt != nil {
		t.Fatalf("flag times %+v", f)
	}
	open, err := w.svc.Flags(t.Context(), "")
	if err != nil || len(open) != 1 || open[0].ID != f.ID {
		t.Fatalf("open flags %+v, %v", open, err)
	}
	// The JSON is snake case, with dismissed_at null while it's open.
	raw, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]any
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatal(err)
	}
	want := []string{"answer", "conversation_id", "created_at", "dismissed_at", "dismissed_by", "id", "note", "org_id",
		"project_id", "question", "reason", "reported_by", "task_id", "updated_at"}
	if got := slices.Sorted(maps.Keys(keys)); !slices.Equal(got, want) || keys["dismissed_at"] != nil {
		t.Fatalf("JSON %s", raw)
	}
}

// Someone who manages the workspace's AI reads everyone's conversations,
// so they may flag an answer in one they didn't start.
func TestFlagByAnAIManager(t *testing.T) {
	w := newWorld(t)
	c := w.open(t, ada, "hello", "")
	w.answered(c.LastTaskID, "Hi.")
	f, err := w.svc.Flag(t.Context(), person(orgA, al), c.ID, c.LastTaskID, FlagInput{Reason: FlagOffensive})
	if err != nil {
		t.Fatal(err)
	}
	if f.ReportedBy != al || f.Reason != "offensive" || f.Note != "" || f.Answer != "Hi." {
		t.Fatalf("flag %+v", f)
	}
}

// Anyone who can't read the conversation gets the error History gives
// them, and nothing is kept.
func TestFlagRefusedForThoseWhoCannotRead(t *testing.T) {
	w := newWorld(t)
	c := w.open(t, ada, "my secret plans", "")
	agent := security.AgentPrincipal(orgA, &security.Principal{Kind: security.KindUser, UserID: ada, OrgID: orgA})
	for _, who := range []security.Principal{person(orgA, bo), person(orgB, bob), agent} {
		_, herr := w.svc.History(t.Context(), who, c.ID, HistoryQuery{})
		_, ferr := w.svc.Flag(t.Context(), who, c.ID, c.LastTaskID, FlagInput{Reason: FlagHarmful})
		if herr == nil || ferr == nil || herr.Error() != ferr.Error() {
			t.Errorf("%s/%s: flag %v, history %v", who.OrgID, who.UserID, ferr, herr)
		}
	}
	if _, err := w.svc.Flag(t.Context(), person(orgA, bo), c.ID, c.LastTaskID, FlagInput{Reason: FlagHarmful}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Bo: %v", err)
	}
	if all, _ := w.svc.Flags(t.Context(), FlagsAll); len(all) != 0 {
		t.Fatalf("kept %+v", all)
	}
	if n := len(w.audit.recorded()); n != 0 {
		t.Fatalf("audited %d", n)
	}
}

// An answer the agent is still writing, or waiting on a person for, isn't
// one to report yet.
func TestFlagRefusedWhileAnswering(t *testing.T) {
	for _, status := range []string{agentruns.StatusQueued, agentruns.StatusRunning, agentruns.StatusWaiting} {
		w := newWorld(t)
		c := w.open(t, ada, "hello", "")
		w.store.setStatus(c.LastTaskID, status)
		if _, err := w.svc.Flag(t.Context(), person(orgA, ada), c.ID, c.LastTaskID, FlagInput{Reason: FlagOther}); !errors.Is(err, ErrUnfinished) {
			t.Errorf("%s: %v, want ErrUnfinished", status, err)
		}
	}
	for _, status := range []string{agentruns.StatusFailed, agentruns.StatusCancelled} {
		w := newWorld(t)
		c := w.open(t, ada, "hello", "")
		w.store.setStatus(c.LastTaskID, status)
		if _, err := w.svc.Flag(t.Context(), person(orgA, ada), c.ID, c.LastTaskID, FlagInput{Reason: FlagOther}); err != nil {
			t.Errorf("%s: %v", status, err)
		}
	}
}

func TestFlagRefusedForATaskOfAnotherConversation(t *testing.T) {
	w := newWorld(t)
	c := w.open(t, ada, "hello", "")
	other := w.open(t, ada, "another", "")
	theirs := w.open(t, bo, "Bo's", "")
	for _, task := range []string{other.LastTaskID, theirs.LastTaskID, "atask_nope", ""} {
		if _, err := w.svc.Flag(t.Context(), person(orgA, ada), c.ID, task, FlagInput{Reason: FlagOther}); !errors.Is(err, ErrNotFound) {
			t.Errorf("task %q: %v, want ErrNotFound", task, err)
		}
	}
}

func TestFlagRefusedOnADeletedConversation(t *testing.T) {
	w := newWorld(t)
	c := w.open(t, ada, "hello", "")
	if err := w.svc.Delete(t.Context(), person(orgA, ada), c.ID); err != nil {
		t.Fatal(err)
	}
	for _, who := range []string{ada, al} {
		if _, err := w.svc.Flag(t.Context(), person(orgA, who), c.ID, c.LastTaskID, FlagInput{Reason: FlagOther}); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: %v, want ErrNotFound", who, err)
		}
	}
}

// Flagging the same answer again changes the reason and note of the one
// flag, and opens it again if it was dismissed.
func TestFlagAgainUpdatesTheFlag(t *testing.T) {
	w := newWorld(t)
	c := w.open(t, ada, "hello", "")
	first, err := w.svc.Flag(t.Context(), person(orgA, ada), c.ID, c.LastTaskID, FlagInput{Reason: FlagOther, Note: "odd"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.svc.DismissFlag(t.Context(), first.ID, "usr_owner"); err != nil {
		t.Fatal(err)
	}
	again, err := w.svc.Flag(t.Context(), person(orgA, ada), c.ID, c.LastTaskID, FlagInput{Reason: FlagHarmful})
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != first.ID || again.Reason != "harmful" || again.Note != "" || !again.CreatedAt.Equal(first.CreatedAt) ||
		!again.UpdatedAt.After(first.UpdatedAt) || again.DismissedAt != nil || again.DismissedBy != "" {
		t.Fatalf("again %+v, first %+v", again, first)
	}
	// Someone else flagging the same answer is a flag of their own.
	if _, err = w.svc.Flag(t.Context(), person(orgA, al), c.ID, c.LastTaskID, FlagInput{Reason: FlagOther}); err != nil {
		t.Fatal(err)
	}
	all, err := w.svc.Flags(t.Context(), FlagsAll)
	if err != nil || len(all) != 2 {
		t.Fatalf("flags %+v, %v", all, err)
	}
}

// The answer kept is the turn's tokens joined, without what a safety
// stop withheld, up to MaxFlagAnswer characters.
func TestFlagKeepsTheAnswer(t *testing.T) {
	w := newWorld(t)
	c := w.open(t, ada, "hello", "")
	evs := []agentruns.Event{
		{Seq: 1, Kind: "run_started", Data: json.RawMessage(`{}`)},
		token(2, "Kept "),
		{Seq: 3, Kind: "token", Data: json.RawMessage(`{"text":"secret","withheld":true}`)},
		{Seq: 4, Kind: "tool_call", Data: json.RawMessage(`{"text":"not an answer"}`)},
		token(5, "and kept."),
		{Seq: 6, Kind: "token", Data: json.RawMessage(`not json`)},
	}
	w.runs.events[c.LastTaskID] = evs
	f, err := w.svc.Flag(t.Context(), person(orgA, ada), c.ID, c.LastTaskID, FlagInput{Reason: FlagOther})
	if err != nil {
		t.Fatal(err)
	}
	if f.Answer != "Kept and kept." {
		t.Fatalf("answer %q", f.Answer)
	}

	// A long answer runs past a page of events and is cut at
	// MaxFlagAnswer characters, not bytes.
	long := w.open(t, ada, "a long one", "")
	texts := make([]string, 1500)
	for i := range texts {
		texts[i] = "héllo, wörld " // 13 characters
	}
	w.answered(long.LastTaskID, texts...)
	f, err = w.svc.Flag(t.Context(), person(orgA, ada), long.ID, long.LastTaskID, FlagInput{Reason: FlagOther})
	if err != nil {
		t.Fatal(err)
	}
	if n := utf8.RuneCountInString(f.Answer); n != MaxFlagAnswer || !utf8.ValidString(f.Answer) ||
		!strings.HasPrefix(f.Answer, "héllo, wörld héllo") {
		t.Fatalf("answer of %d characters", n)
	}
}

func TestFlagIsChecked(t *testing.T) {
	w := newWorld(t)
	c := w.open(t, ada, "hello", "")
	for name, in := range map[string]FlagInput{
		"no reason":    {},
		"bad reason":   {Reason: "spam"},
		"long note":    {Reason: FlagOther, Note: strings.Repeat("é", MaxFlagNote+1)},
		"a control":    {Reason: FlagOther, Note: "bad\x00note"},
		"invalid utf8": {Reason: FlagOther, Note: "bad\xffnote"},
	} {
		if _, err := w.svc.Flag(t.Context(), person(orgA, ada), c.ID, c.LastTaskID, in); !errors.Is(err, ErrBadFlag) {
			t.Errorf("%s: %v, want ErrBadFlag", name, err)
		}
	}
	// A note of MaxFlagNote characters, on more than one line, is fine.
	note := strings.Repeat("é", MaxFlagNote-2) + "\na"
	f, err := w.svc.Flag(t.Context(), person(orgA, ada), c.ID, c.LastTaskID, FlagInput{Reason: FlagOther, Note: note})
	if err != nil || f.Note != note {
		t.Fatalf("a note of MaxFlagNote characters: %v", err)
	}
}

// The audit trail says who flagged or dismissed which answer and why, and
// never what the answer, the question or the note said.
func TestFlagAuditCarriesNoText(t *testing.T) {
	w := newWorld(t)
	c := w.open(t, ada, "QUESTION-TEXT", "proj_1")
	w.answered(c.LastTaskID, "ANSWER-TEXT")
	f, err := w.svc.Flag(t.Context(), person(orgA, ada), c.ID, c.LastTaskID, FlagInput{Reason: FlagHarmful, Note: "NOTE-TEXT"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.svc.DismissFlag(t.Context(), f.ID, "usr_owner"); err != nil {
		t.Fatal(err)
	}
	events := w.audit.recorded()
	if len(events) != 2 || events[0].Action != "agent.answer.flag" || events[1].Action != "agent.answer.flag.dismiss" {
		t.Fatalf("audited %+v", events)
	}
	wantAttrs := map[string]any{"flag_id": f.ID, "conversation_id": c.ID, "task_id": c.LastTaskID, "reason": "harmful"}
	for i, e := range events {
		if !maps.Equal(e.Attributes, wantAttrs) || e.ProjectID != "proj_1" || e.Principal.OrgID != orgA {
			t.Errorf("event %d %+v", i, e)
		}
		dump := fmt.Sprintf("%+v", e)
		for _, text := range []string{"QUESTION-TEXT", "ANSWER-TEXT", "NOTE-TEXT"} {
			if strings.Contains(dump, text) {
				t.Errorf("event %d carries %s: %s", i, text, dump)
			}
		}
	}
	if events[0].Principal.UserID != ada || events[1].Principal.UserID != "usr_owner" {
		t.Fatalf("actors %q, %q", events[0].Principal.UserID, events[1].Principal.UserID)
	}
}

func TestFlagsFilterNewestFirst(t *testing.T) {
	w := newWorld(t)
	ids := make([]string, 0, 3)
	for i := range 3 {
		c := w.open(t, ada, fmt.Sprintf("question %d", i), "")
		f, err := w.svc.Flag(t.Context(), person(orgA, ada), c.ID, c.LastTaskID, FlagInput{Reason: FlagOther})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, f.ID)
	}
	if _, err := w.svc.DismissFlag(t.Context(), ids[1], "usr_owner"); err != nil {
		t.Fatal(err)
	}
	for status, want := range map[string][]string{
		"":             {ids[2], ids[0]},
		FlagsOpen:      {ids[2], ids[0]},
		FlagsAll:       {ids[2], ids[1], ids[0]},
		FlagsDismissed: {ids[1]},
	} {
		got, err := w.svc.Flags(t.Context(), status)
		if err != nil {
			t.Fatalf("%q: %v", status, err)
		}
		gotIDs := make([]string, 0, len(got))
		for _, f := range got {
			gotIDs = append(gotIDs, f.ID)
		}
		if !slices.Equal(gotIDs, want) {
			t.Errorf("%q: %v, want %v", status, gotIDs, want)
		}
	}
	if _, err := w.svc.Flags(t.Context(), "closed"); !errors.Is(err, ErrBadFlag) {
		t.Fatalf("a bad status: %v", err)
	}
	none := newWorld(t)
	if got, err := none.svc.Flags(t.Context(), ""); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("no flags %#v, %v", got, err)
	}
}

func TestDismissFlag(t *testing.T) {
	w := newWorld(t)
	c := w.open(t, ada, "hello", "")
	f, err := w.svc.Flag(t.Context(), person(orgA, ada), c.ID, c.LastTaskID, FlagInput{Reason: FlagOther})
	if err != nil {
		t.Fatal(err)
	}
	d, err := w.svc.DismissFlag(t.Context(), f.ID, "usr_owner")
	if err != nil {
		t.Fatal(err)
	}
	if d.ID != f.ID || d.DismissedBy != "usr_owner" || d.DismissedAt == nil || !d.DismissedAt.After(f.CreatedAt) ||
		!d.UpdatedAt.Equal(*d.DismissedAt) || d.Reason != f.Reason {
		t.Fatalf("dismissed %+v", d)
	}
	// Dismissing it again keeps who dismissed it first, and when.
	again, err := w.svc.DismissFlag(t.Context(), f.ID, "usr_other")
	if err != nil || again.DismissedBy != "usr_owner" || !again.DismissedAt.Equal(*d.DismissedAt) {
		t.Fatalf("again %+v, %v", again, err)
	}
	if _, err = w.svc.DismissFlag(t.Context(), "aflag_nope", "usr_owner"); !errors.Is(err, ErrFlagNotFound) {
		t.Fatalf("an unknown flag: %v", err)
	}
	if _, err = w.svc.DismissFlag(t.Context(), f.ID, ""); !errors.Is(err, ErrBadFlag) {
		t.Fatalf("nobody dismisses: %v", err)
	}
}
