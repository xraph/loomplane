// Package agentsafety scans the text that enters the AI agent's context
// from outside, and the text the agent sends to people, with shield.
//
// Untrusted text (a commit message, a build log, a README, an issue body,
// a tool's result) goes through shield's input scan with the
// infra-agent-input profile, which removes instruction-shaped lines and
// redacts secrets, and comes back quoted with its source so the model
// reads it as data. Outbound text (a report, a question, a notification)
// goes through the infra-agent-output profile. Both fail closed: a block,
// a missing profile, a missing tenant or any other error withholds the
// text or refuses to send it. Neither the errors nor the warnings ever
// carry the scanned text.
//
// Both need a context from agentscope.Binder.Bind, which names the
// workspace as shield's tenant and agentscope.App as its app. The profiles
// exist once the agent's extension has seeded the workspace.
package agentsafety

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/xraph/shield"
	"github.com/xraph/shield/presets"
	"github.com/xraph/shield/scan"
)

// Where untrusted text came from. Shield puts the source on the quote, so
// the model knows what it's reading.
const (
	SourceCommitMessage = "commit_message"
	SourceLogLine       = "log_line"
	SourceReadme        = "readme"
	SourceIssueBody     = "issue_body"
	SourceManifest      = "manifest"
	SourceErrorOutput   = "error_output"
	SourceToolResult    = "tool_result"
)

// Withheld stands in for untrusted text that failed its scan, or couldn't
// be scanned.
const Withheld = "[withheld: this text failed Loomplane's safety scan]"

// DefaultMaxBytes is how much of an untrusted text is scanned when Scanner
// sets no limit: well under shield's 1 MiB default, so the scan never fails
// for size.
const DefaultMaxBytes = 256 << 10

// GuardrailShieldProfile is the agent-config guardrail cortex reads the
// shield profile from, for its own scans and its publication gate.
const GuardrailShieldProfile = "shield_profile"

// cutMarker starts untrusted text whose beginning was cut to fit.
const cutMarker = "[earlier output cut]\n"

// ErrOutboundBlocked is Outbound's answer for text it won't send.
var ErrOutboundBlocked = errors.New("this text was blocked by Loomplane's safety scan and wasn't sent")

// errScanFailed stands in for an engine error that isn't one of shield's
// own, which could carry anything, the text included.
var errScanFailed = errors.New("agentsafety: the safety scan failed")

// Guardrails are the guardrails the agent's cortex config carries: the
// output profile, so cortex never falls back to shield's default profile,
// which Loomplane leaves unset and never seeds. Each call returns a new map.
func Guardrails() map[string]any {
	return map[string]any{GuardrailShieldProfile: presets.InfraAgentOutput}
}

// Engine is what Scanner needs of shield. *shieldengine.Engine is the real
// one.
type Engine interface {
	ScanInput(ctx context.Context, in *scan.Input) (*scan.Result, error)
	ScanOutput(ctx context.Context, in *scan.Input) (*scan.Result, error)
}

// Scanner scans untrusted and outbound text.
type Scanner struct {
	Engine Engine
	// MaxBytes bounds the untrusted text scanned; 0 is DefaultMaxBytes.
	MaxBytes int
	// Warn, when set, hears about every scan that failed rather than
	// decided: msg says which, err is one of shield's errors or a generic
	// one, never the text.
	Warn func(msg string, err error)
}

// Untrusted returns text as the model may read it: quoted with its source
// by shield, with instruction-shaped lines and secrets removed. Text longer
// than MaxBytes keeps its end, cut at a line and marked "[earlier output
// cut]", since the end of a log is where the failure is. Text the scan
// blocks, and text that can't be scanned for any reason, comes back as
// Withheld. Empty text is returned as it is.
func (s *Scanner) Untrusted(ctx context.Context, source, text string) string {
	if text == "" {
		return ""
	}
	if s == nil || s.Engine == nil {
		s.warn("untrusted text wasn't scanned: no safety engine", errScanFailed)
		return Withheld
	}
	res, err := s.Engine.ScanInput(ctx, &scan.Input{
		Text: tail(text, s.maxBytes()), Profile: presets.InfraAgentInput, Source: source, Untrusted: true,
	})
	if err != nil {
		s.warn("untrusted "+source+" text was withheld: the scan failed", sanitize(err))
		return Withheld
	}
	if res == nil || res.Blocked || !passes(res.Decision) || res.Redacted == "" {
		return Withheld
	}
	return res.Redacted
}

// Outbound returns the text the agent may send: as it is when the scan
// allows or flags it, shield's redaction when it redacts. A block, or a
// scan that fails for any reason, is ErrOutboundBlocked, and the caller
// sends nothing.
func (s *Scanner) Outbound(ctx context.Context, text string) (string, error) {
	if s == nil || s.Engine == nil {
		s.warn("outbound text wasn't scanned: no safety engine", errScanFailed)
		return "", ErrOutboundBlocked
	}
	res, err := s.Engine.ScanOutput(ctx, &scan.Input{Text: text, Profile: presets.InfraAgentOutput})
	if err != nil {
		s.warn("outbound text was blocked: the scan failed", sanitize(err))
		return "", ErrOutboundBlocked
	}
	if res == nil || res.Blocked {
		return "", ErrOutboundBlocked
	}
	switch res.Decision {
	case scan.DecisionAllow, scan.DecisionFlag:
		return text, nil
	case scan.DecisionRedact:
		if res.Redacted != "" {
			return res.Redacted, nil
		}
	}
	return "", ErrOutboundBlocked
}

func (s *Scanner) maxBytes() int {
	if s.MaxBytes > 0 {
		return s.MaxBytes
	}
	return DefaultMaxBytes
}

func (s *Scanner) warn(msg string, err error) {
	if s != nil && s.Warn != nil {
		s.Warn(msg, err)
	}
}

// passes reports whether a text scan's decision lets the text through. Any
// other decision, an unknown one included, withholds it.
func passes(d scan.Decision) bool {
	return d == scan.DecisionAllow || d == scan.DecisionFlag || d == scan.DecisionRedact
}

// sanitize keeps an engine error that is one of shield's own, or the
// context's, and replaces anything else, which could carry the text.
func sanitize(err error) error {
	for _, known := range []error{
		shield.ErrNoProfile, shield.ErrTenantRequired, shield.ErrTenantMismatch, shield.ErrInputTooLarge,
		shield.ErrInvalidInput, shield.ErrProfileNotFound, shield.ErrProfileInvalid, shield.ErrNoStore,
		shield.ErrStoreClosed, context.Canceled, context.DeadlineExceeded,
	} {
		if errors.Is(err, known) {
			return known
		}
	}
	return errScanFailed
}

// tail returns the last maxBytes of text, marked as cut, starting at a line
// where one starts in that window and at a character otherwise.
func tail(text string, maxBytes int) string {
	if len(text) <= maxBytes {
		return text
	}
	room := maxBytes - len(cutMarker)
	if room <= 0 {
		return cutMarker
	}
	start := len(text) - room
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	kept := text[start:]
	if text[start-1] != '\n' {
		if i := strings.IndexByte(kept, '\n'); i >= 0 && i+1 < len(kept) {
			kept = kept[i+1:]
		}
	}
	return cutMarker + kept
}
