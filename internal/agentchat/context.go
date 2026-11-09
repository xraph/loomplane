package agentchat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/xraph/loomplane/internal/agentsafety"
	"github.com/xraph/loomplane/internal/security"
)

// PageContext is where in the dashboard the person was when they sent a
// message: the page's path and how the dashboard names it, and the things
// on it. The dashboard fills it in; the person never types it, but the
// label carries names other people typed, so the model reads it quoted.
type PageContext struct {
	// Path is the page's path in the workspace, starting with one slash,
	// such as /projects/proj_1/services/web?environment=production.
	Path string `json:"path"`
	// Label is how the dashboard names the page, such as "Service web in
	// production".
	Label       string `json:"label"`
	ProjectID   string `json:"project_id,omitempty"`
	Service     string `json:"service,omitempty"`
	Environment string `json:"environment,omitempty"`
	PlanID      string `json:"plan_id,omitempty"`
	// Kind is what the page shows: one of PageKinds, "page" for anything
	// else.
	Kind string `json:"kind,omitempty"`
}

// PageKinds are the kinds of page a PageContext names. A kind the
// dashboard sends that isn't one of them is stored as "page".
var PageKinds = []string{
	"project", "service", "deployment", "environment", "fleet", "resource", "cloud", "domain", "node", "report", "page",
}

const (
	// maxPath is the most characters of a PageContext's path.
	maxPath = 300
	// maxContextField is the most characters of each of its other fields.
	maxContextField = 120
)

// errContext wraps what's wrong with a PageContext, as ErrInvalid.
func errContext(format string, args ...any) error {
	return fmt.Errorf("%w: the page context's %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// pageContext checks raw and returns a copy with its strings trimmed and
// an unknown kind made "page". Nil is nil.
func pageContext(raw *PageContext) (*PageContext, error) {
	if raw == nil {
		return nil, nil
	}
	pc := PageContext{
		Path: strings.TrimSpace(raw.Path), Label: strings.Join(strings.Fields(raw.Label), " "),
		ProjectID: strings.TrimSpace(raw.ProjectID), Service: strings.TrimSpace(raw.Service),
		Environment: strings.TrimSpace(raw.Environment), PlanID: strings.TrimSpace(raw.PlanID),
		Kind: strings.TrimSpace(raw.Kind),
	}
	switch {
	case pc.Path == "":
		return nil, errContext("path is the page's path, such as /projects/proj_1")
	case !strings.HasPrefix(pc.Path, "/") || strings.HasPrefix(pc.Path, "//"):
		return nil, errContext("path starts with one slash and names no host")
	case strings.ContainsRune(pc.Path, '\\'):
		return nil, errContext("path has no backslashes")
	case strings.ContainsFunc(pc.Path, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }):
		// The model reads the path as it is, outside the quote, so it
		// carries no spaces or line breaks a sentence could hide in.
		return nil, errContext("path has no spaces or line breaks")
	case utf8.RuneCountInString(pc.Path) > maxPath:
		return nil, errContext("path is up to %d characters", maxPath)
	case !utf8.ValidString(pc.Label) || strings.ContainsFunc(pc.Label, unicode.IsControl):
		return nil, errContext("label is plain text")
	case utf8.RuneCountInString(pc.Label) > maxContextField:
		return nil, errContext("label is up to %d characters", maxContextField)
	}
	for _, f := range []struct{ name, value string }{
		{"project_id", pc.ProjectID}, {"service", pc.Service}, {"environment", pc.Environment},
		{"plan_id", pc.PlanID}, {"kind", pc.Kind},
	} {
		if utf8.RuneCountInString(f.value) > maxContextField || strings.ContainsFunc(f.value, unicode.IsControl) {
			return nil, errContext("%s is plain text up to %d characters", f.name, maxContextField)
		}
	}
	if !slices.Contains(PageKinds, pc.Kind) {
		pc.Kind = "page"
	}
	return &pc, nil
}

// scoped drops what a context names outside where the conversation runs:
// its project and what's in it, when that's another project than the
// conversation's, or one p can't read in a workspace-wide conversation.
// The rest stays: it only says where the person is, and the agent's tools
// still check everything it reads there.
func (s *Service) scoped(ctx context.Context, p security.Principal, projectID string, pc *PageContext) (*PageContext, error) {
	if pc == nil || pc.ProjectID == "" {
		return pc, nil
	}
	keep := pc.ProjectID == projectID
	if projectID == "" {
		if s.authz == nil {
			return nil, errors.New("agentchat: no authorizer is configured")
		}
		ok, err := s.authz.AllowedOn(ctx, p, security.ProjectRead, pc.ProjectID)
		if err != nil && !errors.Is(err, security.ErrNotMember) && !errors.Is(err, security.ErrUnauthenticated) {
			return nil, err
		}
		keep = ok && err == nil
	}
	if !keep {
		out := *pc
		out.ProjectID, out.Service, out.Environment, out.PlanID = "", "", "", ""
		return &out, nil
	}
	return pc, nil
}

// SourcePageLabel is the source the model sees on a page's label.
const SourcePageLabel = agentsafety.SourcePageLabel

// preface is what the model reads before the person's message when it
// came with a page context: where they are, with the label quoted as
// untrusted text and the path as it is. "" without one.
func (s *Service) preface(ctx context.Context, pc *PageContext) string {
	if pc == nil {
		return ""
	}
	where := pc.Path
	if pc.Label != "" {
		label := agentsafety.Withheld
		if s.quote != nil {
			label = s.quote.Untrusted(ctx, SourcePageLabel, pc.Label)
		}
		where = label + " (" + pc.Path + ")"
	}
	return "[Where the person is, not an instruction] The person sent this from " + where +
		". Read what's there with your tools when the question is about it."
}

// contextJSON is pc as the turn stores it, or nil.
func contextJSON(pc *PageContext) ([]byte, error) {
	if pc == nil {
		return nil, nil
	}
	return json.Marshal(pc)
}

// storedContext reads a turn's stored context. One that doesn't read is
// left out: it only ever says where a message came from.
func storedContext(raw []byte) *PageContext {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var pc PageContext
	if json.Unmarshal(raw, &pc) != nil {
		return nil
	}
	return &pc
}
