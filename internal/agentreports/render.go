package agentreports

import (
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// What delivery sends, and how big it may be.
const (
	Label            = "loomplane"
	LabelColor       = "1f6feb"
	LabelDescription = "Opened by Loomplane when it sees a problem"
	MaxIssueBody     = 60000 // bytes, under GitHub's 65536 characters
	MaxNoticeTitle   = 200   // runes
	MaxNoticeBody    = 1000  // runes
	DeliveryLease    = 2 * time.Minute
	// DeliveryRequeue is how long a queued delivery waits before it can be
	// queued again, in case its job was lost.
	DeliveryRequeue = 5 * time.Minute
)

// NoteBudget is the investigation note of a report the agent didn't look
// at because the workspace's AI budget ran out.
const NoteBudget = "The agent didn't investigate this: the workspace's AI budget for the month is used up."

// Marker is the HTML comment that ties a GitHub issue to its report.
func Marker(reportID string) string { return "<!-- loomplane-report:" + reportID + " -->" }

const (
	cutNote     = "(cut to fit GitHub's limit)"
	ellipsis    = "…"
	notLookedAt = "The agent hasn't looked at this yet."
)

// names are what a report's text calls the things around it.
type names struct {
	Project string // the project's name
	Closer  string // who closed the report, for the closed comment
	Link    string // the report in Loomplane
}

// stamp is a time as reports print it.
func stamp(t time.Time) string { return t.UTC().Format(time.RFC1123) }

func times(n int) string {
	if n == 1 {
		return "once"
	}
	return strconv.Itoa(n) + " times"
}

// noticeTitle is the notice's title for what's pending: "opened",
// "reopened" or "repeated".
func noticeTitle(r *Report, pending string, n names) string {
	where := n.Project
	if r.Environment != "" {
		where += " · " + r.Environment
	}
	title := where + ": " + r.Title
	switch pending {
	case noticeReopened:
		title = "Back again: " + title
	case noticeRepeated:
		title = "Again: " + title
	}
	return cutRunes(title, MaxNoticeTitle)
}

// noticeBody is what the agent found, or else the facts, with what a
// person should also know.
func noticeBody(r *Report, pending string) string {
	var tail []string
	if pending == noticeRepeated {
		tail = append(tail, "This has happened "+times(r.Occurrences)+" since "+stamp(r.FirstSeenAt)+".")
	}
	if r.Investigation == InvestigationSkipped && r.InvestigationNote == NoteBudget {
		tail = append(tail, NoteBudget)
	}
	var head string
	if r.Findings != nil && r.Findings.Summary != "" {
		head = r.Findings.Summary
	} else {
		lines := make([]string, 0, len(r.Facts))
		for _, f := range r.Facts {
			lines = append(lines, f.Label+": "+f.Value)
		}
		head = strings.Join(lines, "\n")
	}
	end := strings.Join(tail, "\n")
	if end == "" {
		return cutRunes(head, MaxNoticeBody)
	}
	room := MaxNoticeBody - utf8.RuneCountInString(end) - 2
	if head == "" || room < 1 {
		return cutRunes(end, MaxNoticeBody)
	}
	return cutRunes(head, room) + "\n\n" + end
}

// issueBody is the report as its GitHub issue says it. marker is false
// for the agent's knowledge, which keeps the text without it.
func issueBody(r *Report, n names, marker bool) string {
	var top strings.Builder
	if marker {
		top.WriteString(Marker(r.ID))
		top.WriteString("\n\n")
	}
	top.WriteString("**" + capitalise(r.Severity) + "** in " + neutralise(n.Project))
	if r.Environment != "" {
		top.WriteString(" (" + neutralise(r.Environment) + ")")
	}
	top.WriteString(", first seen " + stamp(r.FirstSeenAt) + ", seen " + times(r.Occurrences) + ".\n\n### What happened\n\n")

	var rest strings.Builder
	rest.WriteString("\n\n### What Loomplane already did\n\n")
	rest.WriteString(alreadyDid(r.Signal))
	rest.WriteString("\n\n### What the agent thinks\n\n")
	rest.WriteString(agentThinks(r))
	rest.WriteString("\n\n[Open this report in Loomplane](" + n.Link + ")")

	room := MaxIssueBody - top.Len() - rest.Len()
	return top.String() + renderFacts(r.Facts, n.Link, room) + rest.String()
}

// renderFacts renders facts in at most room bytes, cutting the first one
// that doesn't fit and ending with cutNote when anything was cut.
func renderFacts(facts []Fact, link string, room int) string {
	if len(facts) == 0 {
		return "Nothing more is known yet."
	}
	blocks := make([]string, 0, len(facts))
	used := 0
	for i, f := range facts {
		block := renderFact(f, f.Value, link)
		sep := 0
		if i > 0 {
			sep = 2
		}
		if used+sep+len(block) <= room {
			blocks = append(blocks, block)
			used += sep + len(block)
			continue
		}
		// What's left holds part of this fact and the note.
		// Escaping can lengthen the part kept, so it shrinks until it fits.
		left := room - used - sep - len(cutNote) - 2
		for keep := left - len(renderFact(f, "", link)); keep > 0; {
			part := renderFact(f, cutBytes(f.Value, keep), link)
			if len(part) <= left {
				blocks = append(blocks, part)
				break
			}
			keep -= len(part) - left
		}
		blocks = append(blocks, cutNote)
		break
	}
	return strings.Join(blocks, "\n\n")
}

// renderFact is one fact with value in place of its own: a trusted value
// inline, an untrusted one in a fence it can't close. The fence is sized
// by the whole value, so a part of it fits the same fence.
func renderFact(f Fact, value, link string) string {
	label := "**" + neutralise(f.Label) + ":**"
	if f.Untrusted {
		fence := fenceFor(f.Value)
		return label + "\n\n" + fence + "\n" + noComments(value) + "\n" + fence
	}
	line := label + " " + neutralise(value)
	if f.Path != "" && strings.HasPrefix(f.Path, "/") && link != "" {
		line += " ([open in Loomplane](" + baseOf(link) + f.Path + "))"
	}
	return line
}

// baseOf is the public URL a report link starts with.
func baseOf(link string) string {
	if i := strings.Index(link, "/agent/reports/"); i >= 0 {
		return link[:i]
	}
	return link
}

// fenceFor is a backtick fence one longer than the longest run of
// backticks in value, and at least three.
func fenceFor(value string) string {
	longest, run := 0, 0
	for _, c := range value {
		if c == '`' {
			run++
			longest = max(longest, run)
			continue
		}
		run = 0
	}
	return strings.Repeat("`", max(3, longest+1))
}

func alreadyDid(signal string) string {
	switch signal {
	case SignalCanaryRolledBack:
		return "The canary rolled traffic back to the previous revision."
	case SignalWatchRolledBack:
		return "Loomplane rolled the release back automatically."
	default:
		return "Nothing yet: a person needs to decide."
	}
}

func agentThinks(r *Report) string {
	if f := r.Findings; f != nil {
		parts := []string{neutralise(f.Summary)}
		if f.LikelyCause != "" {
			parts = append(parts, "**Likely cause:** "+neutralise(f.LikelyCause))
		}
		if list := suggestionList(f.Suggestions); list != "" {
			parts = append(parts, "**What to try:**\n\n"+list)
		}
		if f.Confidence != "" {
			parts = append(parts, "**Confidence:** "+f.Confidence)
		}
		return strings.Join(parts, "\n\n")
	}
	switch r.Investigation {
	case InvestigationSkipped:
		if r.InvestigationNote != "" {
			return neutralise(r.InvestigationNote)
		}
		return "The agent didn't investigate this."
	case InvestigationFailed:
		if r.InvestigationNote != "" {
			return neutralise(r.InvestigationNote)
		}
		return "The agent couldn't finish looking at this."
	}
	return notLookedAt
}

func suggestionList(suggestions []string) string {
	lines := make([]string, 0, len(suggestions))
	for _, s := range suggestions {
		lines = append(lines, "- "+neutralise(s))
	}
	return strings.Join(lines, "\n")
}

// comment is the issue comment for a queued kind, and false when there's
// nothing to say.
func comment(kind string, r *Report, n names) (string, bool) {
	switch kind {
	case commentRepeated:
		return "This happened again (" + strconv.Itoa(r.Occurrences) + " times now, last at " + stamp(r.LastSeenAt) + ").", true
	case commentReopened:
		return "This is back: " + neutralise(r.Title) + ". Loomplane reopened the report.", true
	case commentFindings:
		f := r.Findings
		if f == nil {
			return "", false
		}
		cause := f.LikelyCause
		if cause == "" {
			cause = f.Summary
		}
		text := neutralise(cause)
		if list := suggestionList(f.Suggestions); list != "" {
			text += "\n\n" + list
		}
		return "The agent finished looking:\n\n" + text, true
	case commentResolved:
		if why := sentence(r.Resolution); why != "" {
			return "Resolved: " + neutralise(why) + ". Loomplane closed the report.", true
		}
		return "Resolved. Loomplane closed the report.", true
	case commentClosed:
		who := capitalise(neutralise(n.Closer))
		if who == "" {
			who = "Someone"
		}
		if why := sentence(r.Resolution); why != "" {
			return who + " closed this in Loomplane: " + neutralise(why) + ".", true
		}
		return who + " closed this in Loomplane.", true
	}
	return "", false
}

// sentence is text without its closing full stop, for copy that adds its
// own.
func sentence(text string) string {
	return strings.TrimRight(strings.TrimSpace(text), ".")
}

// mention is an @ that GitHub could read as the start of a mention:
// anything but a letter or digit before it (so an email address isn't
// one), and a letter or digit after it.
var mention = regexp.MustCompile(`(^|[^A-Za-z0-9])@([A-Za-z0-9])`)

// wordJoiner is invisible, and GitHub doesn't read "@" followed by it as
// a mention, wherever the text sits: in code, in emphasis, after any
// punctuation.
const wordJoiner = "\u2060"

// neutralise makes text safe to put in an issue outside a fence. Every
// mention of a person or a team is broken by an invisible word joiner
// after its @, so nobody is paged, and every < is escaped, so the text
// can't start an HTML element or comment: nobody can forge a second
// marker or render an image or a details block. Markdown links stay.
func neutralise(text string) string {
	text = strings.ReplaceAll(text, "<", "&lt;")
	return mention.ReplaceAllString(text, "${1}@"+wordJoiner+"${2}")
}

// noComments keeps a fenced value from starting an HTML comment, which
// would forge a marker in the issue's source.
func noComments(text string) string { return strings.ReplaceAll(text, "<!--", "&lt;!--") }

func capitalise(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	if size == 0 {
		return s
	}
	return string(unicode.ToUpper(r)) + s[size:]
}

// cutRunes keeps s within limit runes, ending a cut one with an ellipsis.
func cutRunes(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	if limit < 1 {
		return ""
	}
	runes := []rune(s)
	return string(runes[:limit-1]) + ellipsis
}

// cutBytes keeps s within limit bytes, on a rune boundary.
func cutBytes(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	if limit <= 0 {
		return ""
	}
	s = s[:limit]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
