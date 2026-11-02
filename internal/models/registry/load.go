package registry

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// qualificationFile holds qualification results, not a family, so Load
// reads it with its own decoder once every family is in.
const qualificationFile = "qualification.yaml"

// dataFS holds every file in data/: the pinned family files and
// qualification.yaml. It embeds the whole directory, not *.yaml, so a stray
// .yml file reaches Load and fails it instead of dropping out unseen.
// Entries waiting for their first pin live in unpinned/, a sibling of data/,
// so they stay out of the binary.
//
//go:embed data
var dataFS embed.FS

var defaultRegistry = mustLoadDefault()

func mustLoadDefault() *Registry {
	sub, err := fs.Sub(dataFS, "data")
	if err != nil {
		panic(fmt.Sprintf("registry: embedded data: %v", err))
	}
	r, err := Load(sub)
	if err != nil {
		panic(fmt.Sprintf("registry: embedded data: %v", err))
	}
	return r
}

// Registry holds the loaded entries, in family order and then file order.
type Registry struct {
	entries []Entry
	byID    map[string]int // index into entries
	results []Result       // from qualification.yaml, in file order
}

// Default returns the registry built into the binary. A bad embedded file
// panics when the package initializes, so a broken registry never ships.
func Default() *Registry { return defaultRegistry }

// Load reads every *.yaml file at the top of fsys. Each file except
// qualification.yaml holds one family, named after the file. A *.yml file is an
// error, so a family can't drop out of the registry over its extension. Decoding is strict, so
// an unknown field is an error, and every entry must pass Validate. IDs are
// unique across files. qualification.yaml, when there is one, is decoded
// just as strictly, and every result must name an entry, quantization and
// GPU kind that exist.
func Load(fsys fs.FS) (*Registry, error) {
	dirEntries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("registry: %w", err)
	}

	type family struct {
		name    string
		entries []Entry
	}
	var families []family
	var problems []error
	for _, d := range dirEntries {
		name := d.Name()
		if d.IsDir() {
			continue
		}
		if path.Ext(name) == ".yml" {
			problems = append(problems, invalidf("%s: name family files .yaml, not .yml", name))
			continue
		}
		if path.Ext(name) != ".yaml" || name == qualificationFile {
			continue
		}
		f, err := readFile(fsys, name)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		families = append(families, family{name: f.Family, entries: f.Entries})
	}
	sort.Slice(families, func(i, j int) bool { return families[i].name < families[j].name })

	r := &Registry{byID: map[string]int{}}
	for _, f := range families {
		for _, e := range f.entries {
			e.Family = f.name
			if i, dup := r.byID[e.ID]; dup {
				problems = append(problems, invalidf("%s: listed in both %s.yaml and %s.yaml", e.ID, r.entries[i].Family, f.name))
				continue
			}
			r.byID[e.ID] = len(r.entries)
			r.entries = append(r.entries, e)
		}
	}
	if len(problems) > 0 {
		return nil, errors.Join(problems...)
	}
	results, qualErr := readQualification(fsys, r)
	if qualErr != nil {
		return nil, qualErr
	}
	r.results = results
	return r, nil
}

// readFile decodes and validates one family file.
func readFile(fsys fs.FS, name string) (File, error) {
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		return File{}, fmt.Errorf("registry: %w", err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var f File
	if err := dec.Decode(&f); err != nil {
		if errors.Is(err, io.EOF) {
			return File{}, invalidf("%s: the file is empty", name)
		}
		return File{}, &invalidError{msg: name + ": " + err.Error(), cause: err}
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return File{}, invalidf("%s: holds more than one YAML document", name)
	}

	if want := strings.TrimSuffix(name, ".yaml"); f.Family != want {
		return File{}, invalidf("family %q in %s: must be %q, the file name", f.Family, name, want)
	}
	var problems []error
	for i := range f.Entries {
		if err := Validate(f.Entries[i]); err != nil {
			problems = append(problems, err)
		}
	}
	if len(problems) > 0 {
		err := errors.Join(problems...)
		return File{}, &invalidError{msg: name + ":\n" + err.Error(), cause: err}
	}
	return f, nil
}

// Entries returns every entry, sorted by family and then by its place in the
// family's file. The entries are copies.
func (r *Registry) Entries() []Entry {
	out := make([]Entry, len(r.entries))
	for i, e := range r.entries {
		out[i] = e.clone()
	}
	return out
}

// Entry returns a copy of the entry with that ID. IDs are case-sensitive.
func (r *Registry) Entry(id string) (Entry, bool) {
	i, ok := r.byID[id]
	if !ok {
		return Entry{}, false
	}
	return r.entries[i].clone(), true
}
