// Package discovery is Loomplane's repository analysis: running the
// detectors over a checkout, and the analysis records an organization sees.
package discovery

import (
	"context"
	"fmt"
	"io/fs"
	"sort"

	"github.com/xraph/loomplane/internal/discovery/detect"
	"github.com/xraph/loomplane/pkg/discovery"
)

// MaxFiles caps how many files one analysis indexes. A checkout bigger
// than this is still analyzed, from the first MaxFiles in walk order, and
// the report says so.
const MaxFiles = 200_000

// Index lists the regular files under fsys a detector may read, sorted.
func Index(ctx context.Context, fsys fs.FS) ([]string, []discovery.Note, error) {
	var files []string
	var notes []discovery.Note
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			notes = append(notes, discovery.Note{Path: p, Message: "unreadable: " + err.Error()})
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if d.IsDir() {
			if p != "." && discovery.SkipDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil // symlinks and devices are never followed
		}
		if len(files) >= MaxFiles {
			return fs.SkipAll
		}
		files = append(files, p)
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("index checkout: %w", err)
	}
	if len(files) >= MaxFiles {
		notes = append(notes, discovery.Note{Message: fmt.Sprintf("indexed the first %d files only", MaxFiles)})
	}
	sort.Strings(files)
	return files, notes, nil
}

// Analyze runs detectors over the checkout in fsys. With no detectors it
// runs Loomplane's own (detect.All).
func Analyze(ctx context.Context, fsys fs.FS, detectors ...discovery.Detector) (*discovery.Report, error) {
	if len(detectors) == 0 {
		detectors = detect.All()
	}
	files, notes, err := Index(ctx, fsys)
	if err != nil {
		return nil, err
	}
	repo := discovery.Repo{FS: fsys, Files: files}
	r := &discovery.Report{Notes: notes}
	for _, d := range detectors {
		if err := d.Detect(ctx, repo, r); err != nil {
			return nil, fmt.Errorf("%s detector: %w", d.Name(), err)
		}
	}
	normalize(r)
	return r, nil
}

// normalize sorts the report and fills empty lists, so the same checkout
// always gives byte-identical JSON.
func normalize(r *discovery.Report) {
	sort.SliceStable(r.Units, func(i, j int) bool { return r.Units[i].ID < r.Units[j].ID })
	sort.SliceStable(r.Workspaces, func(i, j int) bool {
		if r.Workspaces[i].Path != r.Workspaces[j].Path {
			return r.Workspaces[i].Path < r.Workspaces[j].Path
		}
		return r.Workspaces[i].Tool < r.Workspaces[j].Tool
	})
	sort.SliceStable(r.Images, func(i, j int) bool { return r.Images[i].Path < r.Images[j].Path })
	sort.SliceStable(r.Resources, func(i, j int) bool {
		if r.Resources[i].Kind != r.Resources[j].Kind {
			return r.Resources[i].Kind < r.Resources[j].Kind
		}
		return r.Resources[i].Evidence.Path < r.Resources[j].Evidence.Path
	})
	sort.SliceStable(r.BuildSystems, func(i, j int) bool { return r.BuildSystems[i].Path < r.BuildSystems[j].Path })
	sort.SliceStable(r.Notes, func(i, j int) bool { return r.Notes[i].Path < r.Notes[j].Path })
	if r.Units == nil {
		r.Units = []discovery.Unit{}
	}
	if r.Workspaces == nil {
		r.Workspaces = []discovery.Workspace{}
	}
	if r.Images == nil {
		r.Images = []discovery.Image{}
	}
	if r.Resources == nil {
		r.Resources = []discovery.ResourceUse{}
	}
	if r.BuildSystems == nil {
		r.BuildSystems = []discovery.BuildSystem{}
	}
	if r.Notes == nil {
		r.Notes = []discovery.Note{}
	}
}
