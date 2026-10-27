// Command registry-pin pins the model registry to exact Hugging Face
// revisions. It's a dev tool: CI never runs it and the server never imports
// it.
//
//	go run ./internal/models/registry/cmd/registry-pin [-write] [-family qwen] [-hf-token-env HF_TOKEN]
//
// For every entry, and every quantization whose weights live in another
// repo, it reads the repo's current commit from the Hugging Face API and
// fills revision, weight_bytes, parameters, gated, the architecture block
// and context_length from that commit, then works out min_gpu_memory_gib or
// min_memory_gib with the registry's memory math. Titles, summaries, tasks,
// licenses, parsers, prefixes, args and core are written by hand, and the
// tool never changes them.
//
// New entries go in internal/models/registry/unpinned/<family>.yaml with
// the hand-written fields only, because the registry refuses an entry
// without a revision. -write pins each one and moves it to the end of
// data/<family>.yaml. A gated repo needs a token whose account accepted the
// license, so without one its entries wait in unpinned/.
//
// Without -write the tool changes nothing: it prints what would change and
// exits 1 if anything would, or if an entry can't be pinned.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/xraph/loomplane/internal/models/registry"
)

// dataHeader opens every pinned family file the tool writes.
const dataHeader = `# Written by hand: id, title, publisher, summary, license, tasks,
# active_parameters, the embedding block, tool_parser, reasoning_parser,
# trust_remote_code, core, and each quantization's id, engine, repo, file
# and args.
# Filled by go run ./internal/models/registry/cmd/registry-pin -write:
# revision, gated, parameters, context_length, architecture, and each
# quantization's revision, weight_bytes, min_gpu_memory_gib and
# min_memory_gib.
`

// unpinnedHeader opens every unpinned family file the tool writes.
const unpinnedHeader = `# Entries waiting for their first pin. registry-pin -write moves each one
# into data/ once it can read the repo; these are gated, so that needs a
# Hugging Face token whose account accepted the license (-hf-token-env).
`

type options struct {
	write       bool
	family      string
	token       string
	dataDir     string
	unpinnedDir string
	hubURL      string
	interval    time.Duration
}

func main() {
	var o options
	tokenEnv := "HF_TOKEN"
	flag.BoolVar(&o.write, "write", false, "write the pinned fields instead of printing what would change")
	flag.StringVar(&o.family, "family", "", "pin only this family, like qwen")
	flag.StringVar(&tokenEnv, "hf-token-env", tokenEnv, "the environment variable that holds a Hugging Face token, for gated repos")
	flag.StringVar(&o.dataDir, "data", "internal/models/registry/data", "the pinned family files")
	flag.StringVar(&o.unpinnedDir, "unpinned", "internal/models/registry/unpinned", "family files of entries waiting for their first pin")
	flag.StringVar(&o.hubURL, "hf-url", "https://huggingface.co", "the Hugging Face Hub")
	flag.DurationVar(&o.interval, "interval", 400*time.Millisecond, "the least time between two requests to the Hub")
	flag.Parse()
	o.token = os.Getenv(tokenEnv)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code, err := run(ctx, o, os.Stdout)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "registry-pin:", err)
		os.Exit(2)
	}
	os.Exit(code)
}

// familyFile is one family file and where it lives.
type familyFile struct {
	path    string
	header  string
	file    registry.File
	changed bool
}

// report collects what a run found, in the order it found it.
type report struct {
	changes, waiting, problems []string
}

// run pins every entry and returns the exit code: 0 when nothing changed
// (or -write wrote it all), 1 when something differs or can't be pinned.
func run(ctx context.Context, o options, out io.Writer) (int, error) {
	data, err := readFamilies(o.dataDir, dataHeader)
	if err != nil {
		return 0, err
	}
	unpinned, err := readFamilies(o.unpinnedDir, unpinnedHeader)
	if err != nil {
		return 0, err
	}
	p := &pinner{hub: newHub(o.hubURL, o.token, o.interval)}
	var r report

	pinned := map[string]bool{}
	for _, name := range sortedKeys(data) {
		f := data[name]
		for i, e := range f.file.Entries {
			pinned[e.ID] = true
			if o.family != "" && name != o.family {
				continue
			}
			got, pinErr := p.pin(ctx, e)
			if pinErr != nil {
				r.problems = append(r.problems, fmt.Sprintf("%s.yaml %s: %v", name, e.ID, pinErr))
				continue
			}
			if d := diff(e, got); len(d) > 0 {
				for _, line := range d {
					r.changes = append(r.changes, fmt.Sprintf("%s.yaml %s: %s", name, e.ID, line))
				}
				f.file.Entries[i] = got
				f.changed = true
			}
		}
	}

	for _, name := range sortedKeys(unpinned) {
		if o.family != "" && name != o.family {
			continue
		}
		f := unpinned[name]
		var waiting []registry.Entry
		for _, e := range f.file.Entries {
			if pinned[e.ID] {
				r.problems = append(r.problems, fmt.Sprintf("unpinned/%s.yaml %s: already pinned in data/", name, e.ID))
				waiting = append(waiting, e)
				continue
			}
			got, pinErr := p.pin(ctx, e)
			switch {
			case errors.Is(pinErr, errNeedsToken) && o.token == "":
				r.waiting = append(r.waiting, fmt.Sprintf("unpinned/%s.yaml %s: gated, waiting for a Hugging Face token", name, e.ID))
				waiting = append(waiting, e)
				continue
			case pinErr != nil:
				r.problems = append(r.problems, fmt.Sprintf("unpinned/%s.yaml %s: %v", name, e.ID, pinErr))
				waiting = append(waiting, e)
				continue
			}
			r.changes = append(r.changes, fmt.Sprintf("%s.yaml %s: pinned at %s; moves out of unpinned/", name, e.ID, got.Revision))
			target, ok := data[name]
			if !ok {
				target = &familyFile{path: filepath.Join(o.dataDir, name+".yaml"), header: dataHeader, file: registry.File{Family: name}}
				data[name] = target
			}
			target.file.Entries = append(target.file.Entries, got)
			target.changed = true
			f.changed = true
		}
		f.file.Entries = waiting
	}

	r.print(out)
	if o.write {
		for _, name := range sortedKeys(data) {
			if err := writeFamily(data[name]); err != nil {
				return 0, err
			}
		}
		for _, name := range sortedKeys(unpinned) {
			if err := writeFamily(unpinned[name]); err != nil {
				return 0, err
			}
		}
	}
	if len(r.problems) > 0 || (!o.write && len(r.changes) > 0) {
		return 1, nil
	}
	return 0, nil
}

func (r report) print(out io.Writer) {
	section := func(title string, lines []string) {
		if len(lines) == 0 {
			return
		}
		fmt.Fprintf(out, "%s (%d):\n", title, len(lines))
		for _, l := range lines {
			fmt.Fprintln(out, "  "+l)
		}
	}
	section("Changes", r.changes)
	section("Waiting for a token", r.waiting)
	section("Can't pin", r.problems)
	if len(r.changes)+len(r.waiting)+len(r.problems) == 0 {
		fmt.Fprintln(out, "Every entry is pinned to its repo's current commit.")
	}
}

// diff lists the measured fields that differ between old and pinned, one
// line each, as "field: old -> new".
func diff(old, pinned registry.Entry) []string {
	var out []string
	add := func(field string, a, b any) {
		if !reflect.DeepEqual(a, b) {
			out = append(out, fmt.Sprintf("%s: %v -> %v", field, a, b))
		}
	}
	add("revision", old.Revision, pinned.Revision)
	add("gated", old.Gated, pinned.Gated)
	add("parameters", old.Parameters, pinned.Parameters)
	add("context_length", old.ContextLength, pinned.ContextLength)
	add("architecture", fmt.Sprintf("%+v", old.Architecture), fmt.Sprintf("%+v", pinned.Architecture))
	for i, q := range old.Quantizations {
		n := pinned.Quantizations[i]
		add(q.ID+" revision", q.Revision, n.Revision)
		add(q.ID+" weight_bytes", q.WeightBytes, n.WeightBytes)
		add(q.ID+" min_gpu_memory_gib", q.MinGPUMemoryGiB, n.MinGPUMemoryGiB)
		add(q.ID+" min_memory_gib", q.MinMemoryGiB, n.MinMemoryGiB)
	}
	return out
}

// readFamilies decodes every family file in dir as strictly as the
// registry does, but without its checks, since unpinned entries can't pass
// them yet. A missing dir holds no families.
func readFamilies(dir, header string) (map[string]*familyFile, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, err
	}
	out := map[string]*familyFile{}
	for _, path := range paths {
		name := strings.TrimSuffix(filepath.Base(path), ".yaml")
		if name == "qualification" {
			continue
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil, readErr
		}
		dec := yaml.NewDecoder(bytes.NewReader(raw))
		dec.KnownFields(true)
		var f registry.File
		if decErr := dec.Decode(&f); decErr != nil {
			return nil, fmt.Errorf("%s: %w", path, decErr)
		}
		if f.Family != name {
			return nil, fmt.Errorf("%s: family %q, want %q", path, f.Family, name)
		}
		out[name] = &familyFile{path: path, header: header, file: f}
	}
	return out, nil
}

// writeFamily writes a changed family file, or removes it when it holds no
// entries any more.
func writeFamily(f *familyFile) error {
	if !f.changed {
		return nil
	}
	if len(f.file.Entries) == 0 {
		return os.Remove(f.path)
	}
	var buf bytes.Buffer
	buf.WriteString(f.header)
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(f.file); err != nil {
		return fmt.Errorf("%s: %w", f.path, err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("%s: %w", f.path, err)
	}
	return os.WriteFile(f.path, buf.Bytes(), 0o600)
}

func sortedKeys(m map[string]*familyFile) []string {
	return slices.Sorted(maps.Keys(m))
}
