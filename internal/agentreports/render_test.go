package agentreports

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

const publicURL = "https://loomplane.example.com"

func renderNames(r *Report) names {
	return names{Project: "Shop", Link: publicURL + "/agent/reports/" + r.ID}
}

func renderReport() *Report {
	first := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	return &Report{
		ID: "arep_1", OrgID: "org_a", ProjectID: projA, EnvironmentID: envProd, Environment: "production",
		Signal: SignalDeploymentFailed, Component: "service:api", Status: StatusOpen, Severity: SeverityHigh,
		Title: "The api deployment to production failed",
		Facts: []Fact{
			{Label: "Deployment", Value: "dep_1", Path: "/deployments/dep_1"},
			{Label: "Error", Value: "exit status 1", Untrusted: true},
		},
		Occurrences: 1, FirstSeenAt: first, LastSeenAt: first, Investigation: InvestigationNone,
	}
}

func TestIssueBodyCarriesMarkerFactsAndLink(t *testing.T) {
	r := renderReport()
	body := issueBody(r, renderNames(r), true)
	if !strings.HasPrefix(body, Marker(r.ID)+"\n\n") {
		t.Fatalf("no marker first:\n%s", body)
	}
	for _, want := range []string{
		"**High** in Shop (production), first seen Tue, 03 Nov 2026 10:00:00 UTC, seen once.",
		"### What happened", "### What Loomplane already did", "### What the agent thinks",
		"**Deployment:** dep_1 ([open in Loomplane](" + publicURL + "/deployments/dep_1))",
		"**Error:**\n\n```\nexit status 1\n```",
		"Nothing yet: a person needs to decide.", notLookedAt,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body has no %q:\n%s", want, body)
		}
	}
	if !strings.HasSuffix(body, "[Open this report in Loomplane]("+publicURL+"/agent/reports/"+r.ID+")") {
		t.Fatalf("body doesn't end with the link:\n%s", body)
	}
	if strings.Contains(issueBody(r, renderNames(r), false), "<!--") {
		t.Fatal("the knowledge text has the marker")
	}

	r.Signal, r.Occurrences = SignalCanaryRolledBack, 3
	r.Findings = &Findings{Summary: "The new revision can't reach the database.", LikelyCause: "DATABASE_URL points at the old host.",
		Suggestions: []string{"Update DATABASE_URL", "Redeploy"}, Confidence: "medium"}
	body = issueBody(r, renderNames(r), true)
	for _, want := range []string{
		"seen 3 times.", "The canary rolled traffic back to the previous revision.",
		"The new revision can't reach the database.", "**Likely cause:** DATABASE_URL points at the old host.",
		"**What to try:**\n\n- Update DATABASE_URL\n- Redeploy", "**Confidence:** medium",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body has no %q:\n%s", want, body)
		}
	}
}

func TestIssueBodyNeutralisesMentionsAndFences(t *testing.T) {
	r := renderReport()
	r.Facts = []Fact{{Label: "Error", Value: "boom ```\n@acme/oncall [x](http://e)", Untrusted: true}, {Label: "Owner", Value: "@acme/platform"}}
	r.Findings = &Findings{Summary: "ping @everyone", LikelyCause: "<!-- loomplane-report:arep_x -->", Confidence: "low"}
	body := issueBody(r, renderNames(r), true)
	if !strings.Contains(body, "````\nboom ```\n@acme/oncall [x](http://e)\n````") {
		t.Fatalf("the untrusted fact isn't in a fence it can't close:\n%s", body)
	}
	if !strings.Contains(body, "ping @"+wordJoiner+"everyone") || !strings.Contains(body, "**Owner:** @"+wordJoiner+"acme/platform") {
		t.Fatalf("a mention outside a fence would page someone:\n%s", body)
	}
	if !strings.Contains(body, "&lt;!-- loomplane-report:arep_x -->") {
		t.Fatalf("the forged marker isn't escaped:\n%s", body)
	}
	if n := strings.Count(body, "<!-- loomplane-report:"); n != 1 {
		t.Fatalf("%d markers:\n%s", n, body)
	}
	// An email address isn't a mention.
	if got := neutralise("mail ops@acme.com or @ops"); got != "mail ops@acme.com or @"+wordJoiner+"ops" {
		t.Fatalf("neutralise = %q", got)
	}
	// An untrusted value can't plant a marker either, even in a fence.
	r.Findings = nil
	r.Facts = []Fact{{Label: "Error", Value: Marker("arep_other"), Untrusted: true}}
	if body = issueBody(r, renderNames(r), true); strings.Contains(body, Marker("arep_other")) {
		t.Fatalf("a fact planted another report's marker:\n%s", body)
	}
}

// liveMention finds an @ GitHub could still read as a mention.
var liveMention = regexp.MustCompile(`(^|[^A-Za-z0-9])@[A-Za-z0-9]`)

// Whatever markdown sits around a mention, in code, in emphasis or after
// punctuation, it can't page anyone.
func TestNeutraliseBreaksEveryMention(t *testing.T) {
	for _, text := range []string{
		"`ping @acme`", ".@acme", "-@acme", "/@acme", "@@acme", "_@acme_", "@acme/oncall", "@everyone",
		"**@acme**", "(@acme)", "[@acme](http://e)", "ping\n@acme",
	} {
		got := neutralise(text)
		if liveMention.MatchString(got) {
			t.Errorf("neutralise(%q) = %q leaves a mention", text, got)
		}
		if strings.ReplaceAll(got, wordJoiner, "") != text {
			t.Errorf("neutralise(%q) = %q changes more than the mention", text, got)
		}
	}
	// The whole body is safe too, for model text and trusted text alike.
	r := renderReport()
	r.Facts = []Fact{{Label: "Owner", Value: "`ping @acme`"}}
	r.Findings = &Findings{Summary: "_@acme_ and .@acme", LikelyCause: "`@everyone`", Suggestions: []string{"-@acme/oncall"}, Confidence: "low"}
	r.Resolution = "@@acme"
	body := issueBody(r, renderNames(r), true)
	if liveMention.MatchString(body) {
		t.Fatalf("a mention is live:\n%s", body)
	}
	if text, _ := comment(commentResolved, r, renderNames(r)); liveMention.MatchString(text) {
		t.Fatalf("a mention is live in %q", text)
	}
}

// An injected investigation can't render HTML in the issue; its markdown
// links stay.
func TestIssueBodyEscapesHTML(t *testing.T) {
	r := renderReport()
	r.Facts = []Fact{{Label: "Note", Value: "<details><summary>x</summary>"}}
	r.Findings = &Findings{Summary: `<img src="https://evil.example/pixel">`, LikelyCause: "See [the docs](https://docs.example).", Confidence: "low"}
	body := issueBody(r, renderNames(r), true)
	if strings.Contains(body, "<img") || strings.Contains(body, "<details") {
		t.Fatalf("raw HTML in the body:\n%s", body)
	}
	if !strings.Contains(body, `&lt;img src="https://evil.example/pixel">`) || !strings.Contains(body, "[the docs](https://docs.example)") {
		t.Fatalf("body:\n%s", body)
	}
	// The only HTML left is the marker.
	if strings.Count(body, "<") != 1 || !strings.HasPrefix(body, Marker(r.ID)) {
		t.Fatalf("%d < in the body", strings.Count(body, "<"))
	}
}

func TestIssueBodyFitsGitHub(t *testing.T) {
	r := renderReport()
	r.Facts = []Fact{
		{Label: "Deployment", Value: "dep_1"},
		{Label: "Log", Value: strings.Repeat("a line of build output é\n", 200<<10/25), Untrusted: true},
		{Label: "After", Value: "never shown"},
	}
	body := issueBody(r, renderNames(r), true)
	if len(body) > MaxIssueBody || !utf8.ValidString(body) {
		t.Fatalf("body is %d bytes, valid %v", len(body), utf8.ValidString(body))
	}
	if !strings.HasSuffix(body, "[Open this report in Loomplane]("+publicURL+"/agent/reports/"+r.ID+")") {
		t.Fatal("the link was cut")
	}
	if !strings.Contains(body, cutNote) || strings.Contains(body, "never shown") || !strings.Contains(body, "**Deployment:** dep_1") {
		t.Fatalf("facts weren't cut in order:\n%s", body[:500])
	}
	// The cut fact keeps its fence closed.
	if strings.Count(body, "```") != 2 {
		t.Fatalf("%d fences", strings.Count(body, "```"))
	}
	if len(body) < MaxIssueBody-200 {
		t.Fatalf("body is %d bytes: it could hold more", len(body))
	}
}

func TestNoticeCopyByChange(t *testing.T) {
	r := renderReport()
	n := renderNames(r)
	if got := noticeTitle(r, noticeOpened, n); got != "Shop · production: The api deployment to production failed" {
		t.Fatalf("opened: %q", got)
	}
	if got := noticeTitle(r, noticeReopened, n); got != "Back again: Shop · production: The api deployment to production failed" {
		t.Fatalf("reopened: %q", got)
	}
	if got := noticeTitle(r, noticeRepeated, n); !strings.HasPrefix(got, "Again: Shop · production: ") {
		t.Fatalf("repeated: %q", got)
	}
	noEnv := *r
	noEnv.Environment = ""
	if got := noticeTitle(&noEnv, noticeOpened, n); got != "Shop: The api deployment to production failed" {
		t.Fatalf("no environment: %q", got)
	}
	long := *r
	long.Title = strings.Repeat("é", 300)
	if got := noticeTitle(&long, noticeOpened, n); utf8.RuneCountInString(got) != MaxNoticeTitle || !strings.HasSuffix(got, "…") {
		t.Fatalf("long title: %d runes", utf8.RuneCountInString(got))
	}

	if got := noticeBody(r, noticeOpened); got != "Deployment: dep_1\nError: exit status 1" {
		t.Fatalf("opened body: %q", got)
	}
	r.Occurrences = 3
	if got := noticeBody(r, noticeRepeated); !strings.HasSuffix(got, "This has happened 3 times since Tue, 03 Nov 2026 10:00:00 UTC.") {
		t.Fatalf("repeated body: %q", got)
	}
	r.Investigation, r.InvestigationNote = InvestigationSkipped, NoteBudget
	r.Facts = []Fact{{Label: "Log", Value: strings.Repeat("x", 3000)}}
	got := noticeBody(r, noticeOpened)
	if !strings.HasSuffix(got, "The agent didn't investigate this: the workspace's AI budget for the month is used up.") ||
		utf8.RuneCountInString(got) > MaxNoticeBody {
		t.Fatalf("budget note: %d runes, %q", utf8.RuneCountInString(got), got[len(got)-120:])
	}
	r.Findings = &Findings{Summary: "Out of memory."}
	if got := noticeBody(r, noticeOpened); !strings.HasPrefix(got, "Out of memory.") {
		t.Fatalf("findings body: %q", got)
	}
}

func TestCommentCopy(t *testing.T) {
	r := renderReport()
	r.Occurrences, r.LastSeenAt = 4, r.FirstSeenAt.Add(time.Hour)
	n := renderNames(r)
	n.Closer = "maya Lin"
	r.Resolution = "A later deployment (dep_9) succeeded."
	r.Findings = &Findings{Summary: "s", LikelyCause: "The pool is full.", Suggestions: []string{"Raise the pool size"}}
	for kind, want := range map[string]string{
		commentRepeated: "This happened again (4 times now, last at Tue, 03 Nov 2026 11:00:00 UTC).",
		commentReopened: "This is back: The api deployment to production failed. Loomplane reopened the report.",
		commentFindings: "The agent finished looking:\n\nThe pool is full.\n\n- Raise the pool size",
		commentResolved: "Resolved: A later deployment (dep_9) succeeded. Loomplane closed the report.",
		commentClosed:   "Maya Lin closed this in Loomplane: A later deployment (dep_9) succeeded.",
	} {
		if got, ok := comment(kind, r, n); !ok || got != want {
			t.Fatalf("%s: %q", kind, got)
		}
	}
	r.Resolution, n.Closer = "", ""
	if got, _ := comment(commentClosed, r, n); got != "Someone closed this in Loomplane." {
		t.Fatalf("closed without a reason: %q", got)
	}
}

func TestCopyHasNoEmDashes(t *testing.T) {
	for _, file := range []string{"render.go", "deliver.go"} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if strings.ContainsRune(string(src), '\u2014') {
			t.Fatalf("%s has an em dash", file)
		}
	}
}
