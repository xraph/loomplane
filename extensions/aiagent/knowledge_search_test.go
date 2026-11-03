package aiagent

import (
	"context"
	"errors"
	"strings"
	"testing"

	cortexengine "github.com/xraph/cortex/engine"
	"github.com/xraph/cortex/knowledge"
)

// failingKnowledge fails every call the way cortex's weave adapter does
// before the docs collection exists.
type failingKnowledge struct{ fail bool }

var errNoDocs = errors.New(`weave: resolve shared collection "loomplane-docs": collection not found: secret-host:5432`)

func (f failingKnowledge) Retrieve(context.Context, string, *knowledge.RetrieveParams) ([]knowledge.ScoredChunk, error) {
	if f.fail {
		return nil, errNoDocs
	}
	return []knowledge.ScoredChunk{{Content: "docs"}}, nil
}

func (f failingKnowledge) ListCollections(context.Context) ([]knowledge.CollectionInfo, error) {
	if f.fail {
		return nil, errNoDocs
	}
	return []knowledge.CollectionInfo{{Name: "loomplane-docs"}}, nil
}

// A failed knowledge search reaches the model as a short fixed message,
// never the library's error, which the log keeps.
func TestKnowledgeSearchErrorsAreQuiet(t *testing.T) {
	var logged []error
	warn := func(_ string, err error) { logged = append(logged, err) }
	eng, err := cortexengine.New(cortexengine.WithKnowledge(failingKnowledge{fail: true}), quietKnowledgeOption(warn))
	if err != nil {
		t.Fatal(err)
	}
	_, err = eng.Knowledge().Retrieve(t.Context(), "canary thresholds", nil)
	if !errors.Is(err, errSearchNotReady) || strings.Contains(err.Error(), "secret-host") || strings.Contains(err.Error(), "weave") {
		t.Fatalf("Retrieve: %v", err)
	}
	if _, err = eng.Knowledge().ListCollections(t.Context()); !errors.Is(err, errSearchNotReady) {
		t.Fatalf("ListCollections: %v", err)
	}
	if len(logged) != 2 || !errors.Is(logged[0], errNoDocs) {
		t.Fatalf("logged %v", logged)
	}

	// A search that works is passed through.
	eng, err = cortexengine.New(cortexengine.WithKnowledge(failingKnowledge{}), quietKnowledgeOption(warn))
	if err != nil {
		t.Fatal(err)
	}
	chunks, err := eng.Knowledge().Retrieve(t.Context(), "q", nil)
	if err != nil || len(chunks) != 1 || chunks[0].Content != "docs" {
		t.Fatalf("Retrieve: %v %v", chunks, err)
	}
	// No knowledge, nothing to wrap.
	if eng, err = cortexengine.New(quietKnowledgeOption(warn)); err != nil || eng.Knowledge() != nil {
		t.Fatalf("without knowledge: %v, %v", eng.Knowledge(), err)
	}
}
