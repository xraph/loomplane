package qualify

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/xraph/loomplane/internal/models/registry"
)

// resultKey is what makes two results the same run: the same entry,
// quantization and weights revision, on the same GPU kind and count, with
// the same engine release. A re-pinned entry has a new revision, so its
// new results never replace, or inherit, the old revision's.
type resultKey struct {
	entry, quantization, revision, kind string
	gpus                                int
	engine                              registry.Engine
	version                             string
}

func keyOf(r registry.Result) resultKey {
	return resultKey{r.Entry, r.Quantization, r.Revision, r.GPUKind, r.GPUs, r.Engine, r.EngineVersion}
}

// Merge adds results into data/qualification.yaml (file), keeping the newest result per (entry, quantization,
// revision, kind, gpus, engine version). A newer result replaces an older one where it stands, results for new keys
// go at the end in a stable order, and every comment in the file stays. Results are never dropped for being old: a
// result for another revision or engine release simply stops counting in registry.FitOn.
func Merge(file []byte, results []registry.Result) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(file, &doc); err != nil {
		return nil, fmt.Errorf("qualification.yaml: %w", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("qualification.yaml: expected a mapping with a results list")
	}
	root := doc.Content[0]
	var seq *yaml.Node
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "results" {
			seq = root.Content[i+1]
		}
	}
	if seq == nil || seq.Kind != yaml.SequenceNode {
		return nil, errors.New("qualification.yaml: expected a results list")
	}
	var existing []registry.Result
	if err := seq.Decode(&existing); err != nil {
		return nil, fmt.Errorf("qualification.yaml: %w", err)
	}

	// The newest incoming result per key.
	incoming := map[resultKey]registry.Result{}
	for _, r := range results {
		k := keyOf(r)
		if old, ok := incoming[k]; !ok || r.RanAt.After(old.RanAt) {
			incoming[k] = r
		}
	}
	for i, old := range existing {
		k := keyOf(old)
		r, ok := incoming[k]
		if !ok {
			continue
		}
		delete(incoming, k)
		if !r.RanAt.After(old.RanAt) {
			continue
		}
		node, err := resultNode(r)
		if err != nil {
			return nil, err
		}
		node.HeadComment, node.LineComment, node.FootComment = seq.Content[i].HeadComment, seq.Content[i].LineComment, seq.Content[i].FootComment
		seq.Content[i] = node
	}
	added := make([]registry.Result, 0, len(incoming))
	for _, r := range incoming {
		added = append(added, r)
	}
	sort.Slice(added, func(i, j int) bool {
		a, b := added[i], added[j]
		if !a.RanAt.Equal(b.RanAt) {
			return a.RanAt.Before(b.RanAt)
		}
		return fmt.Sprint(keyOf(a)) < fmt.Sprint(keyOf(b))
	})
	for _, r := range added {
		node, err := resultNode(r)
		if err != nil {
			return nil, err
		}
		seq.Content = append(seq.Content, node)
	}
	if len(seq.Content) > 0 {
		seq.Style = 0 // block style once there's something in it
	}

	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func resultNode(r registry.Result) (*yaml.Node, error) {
	r.RanAt = r.RanAt.UTC()
	var node yaml.Node
	if err := node.Encode(r); err != nil {
		return nil, err
	}
	return &node, nil
}

// secretPatterns catch secrets the job didn't hand out itself: Hugging
// Face tokens, model keys and bearer headers an engine might echo.
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`hf_[A-Za-z0-9]{8,}`),
	regexp.MustCompile(`lpm_[A-Za-z0-9_-]{8,}`),
	regexp.MustCompile(`(?i)(bearer\s+)[^\s"']+`),
}

// redact replaces every secret in s, and anything that looks like one,
// with [redacted].
func redact(s string, secrets ...string) string {
	for _, secret := range secrets {
		if len(secret) >= 4 {
			s = strings.ReplaceAll(s, secret, "[redacted]")
		}
	}
	for i, p := range secretPatterns {
		if i == len(secretPatterns)-1 {
			s = p.ReplaceAllString(s, "${1}[redacted]")
			continue
		}
		s = p.ReplaceAllString(s, "[redacted]")
	}
	return s
}

// logTailBytes is how much of a failed container's log a result keeps.
const logTailBytes = 400

// failureDetail is why a sample failed, with the last 400 bytes of its
// container log, secrets redacted before the cut so a cut can't leave part
// of one.
func failureDetail(why string, logs []byte, secrets ...string) string {
	detail := redact(why, secrets...)
	tail := redact(string(logs), secrets...)
	if len(tail) > logTailBytes {
		tail = tail[len(tail)-logTailBytes:]
	}
	tail = strings.Join(strings.Fields(strings.ToValidUTF8(tail, "")), " ")
	if tail != "" {
		detail += ". Log: " + tail
	}
	return detail
}

// Summary is the pull request body for a run's report: what passed, what
// failed and what didn't run.
func Summary(rep *Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "The weekly qualification run on %s (%s) for %s.\n\n", rep.Kind, rep.Size, rep.Week)
	if rep.Run != "" {
		fmt.Fprintf(&b, "Run: %s\n\n", rep.Run)
	}
	if rep.Stopped != "" {
		fmt.Fprintf(&b, "The run stopped early: %s\n\n", oneLine(rep.Stopped))
	}
	section := func(title string, rows []string) {
		fmt.Fprintf(&b, "### %s (%d)\n\n", title, len(rows))
		if len(rows) == 0 {
			b.WriteString("None.\n\n")
			return
		}
		for _, r := range rows {
			b.WriteString("- " + r + "\n")
		}
		b.WriteString("\n")
	}
	var passed, failed []string
	notRun := make([]string, 0, len(rep.NotRun))
	for _, r := range rep.Results {
		row := fmt.Sprintf("%s %s on %s: %s", r.Entry, r.Quantization, where(r), oneLine(r.Detail))
		if r.Outcome == registry.OutcomePassed {
			passed = append(passed, row)
		} else {
			failed = append(failed, row)
		}
	}
	for _, s := range rep.NotRun {
		notRun = append(notRun, fmt.Sprintf("%s: %s", s.Sample, oneLine(s.Reason)))
	}
	section("Passed", passed)
	section("Failed", failed)
	section("Not run", notRun)
	b.WriteString("Only passed results make a fit tested, and only on the revision and engine release they ran. Failed results stop the fit being offered on that GPU.\n\n")
	b.WriteString("CPU results (TEI, and llama.cpp on CPU) ran on the GPU Droplet's host CPUs, inside the CPU and memory limits their model kind sets, not on a CPU-only server.\n")
	return b.String()
}

func where(r registry.Result) string {
	if r.GPUKind == registry.CPUKind {
		return "CPU"
	}
	return strconv.Itoa(r.GPUs) + " " + r.GPUKind
}
