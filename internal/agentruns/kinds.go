package agentruns

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/xraph/loomplane/internal/agentsafety"
)

// RegisterKind adds how the agent runs one kind of task. 3c and 3d call it
// from their Register; a kind registered twice is refused.
func (s *Service) RegisterKind(spec KindSpec) error {
	switch {
	case spec.Kind == "":
		return errors.New("agentruns: a kind spec names no kind")
	case spec.Agent == "":
		return fmt.Errorf("agentruns: kind %s names no cortex agent", spec.Kind)
	case spec.MaxSteps <= 0 || spec.DefaultMaxTokens <= 0 || spec.MaxTokens < spec.DefaultMaxTokens:
		return fmt.Errorf("agentruns: kind %s needs steps, a default token budget and a ceiling at least as high", spec.Kind)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.kinds[spec.Kind]; ok {
		return fmt.Errorf("agentruns: kind %s is already registered", spec.Kind)
	}
	spec.Tools = slices.Clone(spec.Tools)
	s.kinds[spec.Kind] = spec
	return nil
}

func (s *Service) kind(k Kind) (KindSpec, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	spec, ok := s.kinds[k]
	return spec, ok
}

// definition is the SHA-256 of what a kind's cortex agent is built from.
// The agent config keeps it in Metadata["definition"], and a config whose
// definition differs is updated before the next run.
func definition(spec KindSpec, tools []string) string {
	raw, err := json.Marshal(struct {
		Prompt    string         `json:"prompt"`
		Tools     []string       `json:"tools"`
		MaxSteps  int            `json:"max_steps"`
		MaxTokens int            `json:"max_tokens"`
		Guard     map[string]any `json:"guardrails"`
	}{spec.Prompt, tools, spec.MaxSteps, spec.outputTokens(), agentsafety.Guardrails()})
	if err != nil {
		// Every field marshals; a failure would make each run update the
		// config, which is harmless.
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
