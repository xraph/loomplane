package deployments

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	gotoken "go/token"
	"go/version"
	"io"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/moby/patternmatcher"
	"github.com/moby/patternmatcher/ignorefile"
	"golang.org/x/mod/modfile"

	"github.com/xraph/loomplane/internal/planning"
)

const maxContextBytes = 128 << 20
const maxContextFile = 32 << 20
const generatedDockerfile = ".loomplane-build/Dockerfile"

type preparedContext struct {
	Tar        []byte
	Dockerfile string
}

type sourceFile struct {
	header tar.Header
	data   []byte
}

func safeBuildPath(value string) bool {
	return value == "." || (fs.ValidPath(value) && !strings.ContainsAny(value, "\\\r\n\x00"))
}

func readImageSource(input io.Reader) (map[string]sourceFile, error) {
	z, err := gzip.NewReader(input)
	if err != nil {
		return nil, fmt.Errorf("read approved source: %w", err)
	}
	defer z.Close()
	reader := tar.NewReader(z)
	files := map[string]sourceFile{}
	root := ""
	total := int64(0)
	entries := 0
	for {
		h, readErr := reader.Next()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, fmt.Errorf("read approved source: %w", readErr)
		}
		entries++
		if entries > 100000 {
			return nil, errors.New("the build source has too many entries")
		}
		name := strings.TrimPrefix(h.Name, "./")
		prefix, rel, found := strings.Cut(name, "/")
		if !found && h.Typeflag == tar.TypeDir {
			prefix, rel = name, ""
		} else if !found {
			return nil, errors.New("the approved source must have one repository root")
		}
		if prefix == "" || prefix == "." || prefix == ".." {
			return nil, errors.New("unsafe build source path")
		}
		if root == "" {
			root = prefix
		} else if root != prefix {
			return nil, errors.New("the approved source has multiple roots")
		}
		if rel == "" && h.Typeflag == tar.TypeDir {
			continue
		}
		rel = strings.TrimSuffix(rel, "/")
		if !safeBuildPath(rel) || rel == "." {
			return nil, fmt.Errorf("unsafe build source path %q", rel)
		}
		if h.Typeflag == tar.TypeDir {
			continue
		}
		if _, exists := files[rel]; exists {
			return nil, fmt.Errorf("duplicate build source path %q", rel)
		}
		copyHeader := *h
		copyHeader.Name = rel
		copyHeader.Uid, copyHeader.Gid = 0, 0
		copyHeader.Uname, copyHeader.Gname = "", ""
		copyHeader.PAXRecords = nil
		switch h.Typeflag {
		case tar.TypeReg:
			if h.Size < 0 || h.Size > maxContextFile || total+h.Size > maxContextBytes {
				return nil, errors.New("the build source exceeds its size limit")
			}
			data, readErr := io.ReadAll(io.LimitReader(reader, maxContextFile+1))
			if readErr != nil {
				return nil, readErr
			}
			if int64(len(data)) != h.Size {
				return nil, errors.New("incomplete build source file")
			}
			total += h.Size
			files[rel] = sourceFile{copyHeader, data}
		case tar.TypeSymlink:
			// The link is checked against the repository boundary and is never extracted.
			if !safeBuildPath(path.Clean(path.Join(path.Dir(rel), h.Linkname))) || path.IsAbs(h.Linkname) || strings.ContainsAny(h.Linkname, "\\\r\n\x00") { //nolint:gosec // G305: normalized links are checked within the source and never extracted.
				return nil, fmt.Errorf("unsafe build source symlink %q", rel)
			}
			files[rel] = sourceFile{header: copyHeader}
		default:
			return nil, fmt.Errorf("unsupported build source entry %q", rel)
		}
	}
	return files, nil
}

func withinBuildContext(name, context string) (string, bool) {
	if context == "." {
		return name, true
	}
	rel, ok := strings.CutPrefix(name, context+"/")
	return rel, ok
}

func prepareImageContext(input io.Reader, cfg *planning.ServiceConfig) (*preparedContext, error) {
	if cfg == nil {
		return nil, errors.New("the service has no reviewed build configuration")
	}
	files, err := readImageSource(input)
	if err != nil {
		return nil, err
	}
	context := cfg.ContextDir()
	if cfg.Image == "" && cfg.Context == "" {
		context = "."
	}
	if !safeBuildPath(context) {
		return nil, errors.New("choose a build context within the approved repository")
	}
	var dockerfile []byte
	if cfg.Image != "" {
		file, found := files[cfg.Image]
		if !safeBuildPath(cfg.Image) || !found || file.header.Typeflag == tar.TypeSymlink {
			return nil, errors.New("the reviewed Dockerfile is missing from the approved source")
		}
		dockerfile = file.data
	} else {
		if cfg.Ecosystem != "go" {
			return nil, errors.New("this source needs a Dockerfile for a remote image build")
		}
		dockerfile, err = forgefile(files, cfg.Source, context)
		if err != nil {
			return nil, err
		}
	}
	ignore := files[path.Join(context, ".dockerignore")].data
	if specific, found := files[cfg.Image+".dockerignore"]; cfg.Image != "" && found {
		ignore = specific.data
	}
	patterns, err := ignorefile.ReadAll(bytes.NewReader(ignore))
	if err != nil {
		return nil, err
	}
	matcher, err := patternmatcher.New(patterns)
	if err != nil {
		return nil, fmt.Errorf("read Docker ignore rules: %w", err)
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	var archive bytes.Buffer
	w := tar.NewWriter(&archive)
	included := map[string]sourceFile{}
	for _, name := range names {
		rel, inContext := withinBuildContext(name, context)
		if !inContext {
			continue
		}
		if rel == generatedDockerfile {
			return nil, errors.New("the source uses Loomplane's reserved build file path")
		}
		excluded, matchErr := matcher.MatchesOrParentMatches(rel)
		if matchErr != nil {
			return nil, matchErr
		}
		if excluded || rel == ".dockerignore" {
			continue
		}
		file := files[name]
		if file.header.Typeflag == tar.TypeSymlink {
			if _, ok := withinBuildContext(path.Clean(path.Join(path.Dir(name), file.header.Linkname)), context); !ok {
				return nil, fmt.Errorf("symlink %q leaves the reviewed build context", rel)
			}
		}
		file.header.Name = rel
		included[rel] = file
		if headerErr := w.WriteHeader(&file.header); headerErr != nil {
			return nil, headerErr
		}
		if _, err = w.Write(file.data); err != nil {
			return nil, err
		}
	}
	if cfg.Image == "" {
		entry := cfg.Source
		if entry == context {
			entry = "."
		} else if context != "." {
			entry = strings.TrimPrefix(entry, context+"/")
		}
		if _, checkErr := forgefile(included, entry, "."); checkErr != nil {
			return nil, fmt.Errorf("docker ignore rules leave an incomplete Go build context: %w", checkErr)
		}
		parent := cfg.Source
		if strings.HasSuffix(parent, ".go") {
			parent = path.Dir(parent)
		}
		for {
			workPath := path.Join(parent, "go.work")
			if _, found := files[workPath]; found {
				rel, inside := withinBuildContext(workPath, context)
				if _, kept := included[rel]; !inside || !kept {
					return nil, errors.New("the build context excludes the Go workspace; include go.work and its modules")
				}
				break
			}
			if parent == "." {
				break
			}
			parent = path.Dir(parent)
		}
	}
	for _, file := range []struct {
		name string
		data []byte
	}{{generatedDockerfile, dockerfile}, {".dockerignore", ignore}} {
		if headerErr := w.WriteHeader(&tar.Header{Name: file.name, Mode: 0o644, Size: int64(len(file.data))}); headerErr != nil {
			return nil, headerErr
		}
		if _, err = w.Write(file.data); err != nil {
			return nil, err
		}
	}
	if closeErr := w.Close(); closeErr != nil {
		return nil, closeErr
	}
	return &preparedContext{Tar: archive.Bytes(), Dockerfile: generatedDockerfile}, nil
}

var goVersionPattern = regexp.MustCompile(`^1\.\d+(?:\.\d+)?$`)

func forgefile(files map[string]sourceFile, source, context string) ([]byte, error) {
	if strings.HasSuffix(source, ".go") {
		source = path.Dir(source)
	}
	if !safeBuildPath(source) {
		return nil, errors.New("the declared Go entrypoint is not a repository path")
	}
	if source != "." {
		if _, ok := withinBuildContext(source, context); !ok {
			return nil, errors.New("the build context does not contain the declared Go entrypoint")
		}
	}
	main := false
	for name, file := range files {
		if path.Dir(name) != source || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(gotoken.NewFileSet(), name, file.data, parser.PackageClauseOnly)
		if err != nil {
			return nil, fmt.Errorf("read declared Go entrypoint: %w", err)
		}
		main = main || parsed.Name.Name == "main"
	}
	if !main {
		return nil, errors.New("the declared Go entrypoint has no main package; choose a Dockerfile or analyze the app again")
	}
	module := source
	for {
		if _, found := files[path.Join(module, "go.mod")]; found {
			break
		}
		if module == "." {
			return nil, errors.New("the declared Go entrypoint has no go.mod")
		}
		module = path.Dir(module)
	}
	if module != context {
		if _, ok := withinBuildContext(module, context); !ok {
			return nil, errors.New("the build context does not contain the Go module")
		}
	}
	mod, err := modfile.Parse(path.Join(module, "go.mod"), files[path.Join(module, "go.mod")].data, nil)
	if err != nil {
		return nil, fmt.Errorf("read the app's Go module: %w", err)
	}
	if mod.Go == nil || !goVersionPattern.MatchString(mod.Go.Version) {
		return nil, errors.New("the Go module needs a stable Go version or an explicit Dockerfile")
	}
	goVersion := mod.Go.Version
	checkModule := func(base, relative string) error {
		if path.IsAbs(relative) {
			return errors.New("the Go build uses an absolute local module path; choose a self-contained Dockerfile context")
		}
		dir := path.Clean(path.Join(base, relative))
		_, inside := withinBuildContext(path.Join(dir, "go.mod"), context)
		if !inside || !safeBuildPath(dir) {
			return errors.New("the build context does not contain a local Go module dependency")
		}
		if _, found := files[path.Join(dir, "go.mod")]; !found {
			return errors.New("a local Go module dependency is absent from the approved source")
		}
		return nil
	}
	for _, replace := range mod.Replace {
		if replace.New.Version == "" {
			if moduleErr := checkModule(module, replace.New.Path); moduleErr != nil {
				return nil, moduleErr
			}
		}
	}
	for parent := module; ; parent = path.Dir(parent) {
		if file, found := files[path.Join(parent, "go.work")]; found {
			work, parseErr := modfile.ParseWork("go.work", file.data, nil)
			if parseErr != nil {
				return nil, parseErr
			}
			if work.Go != nil && version.Compare("go"+work.Go.Version, "go"+goVersion) > 0 {
				goVersion = work.Go.Version
			}
			for _, use := range work.Use {
				if moduleErr := checkModule(parent, use.Path); moduleErr != nil {
					return nil, moduleErr
				}
				member := path.Clean(path.Join(parent, use.Path))
				memberMod, memberErr := modfile.Parse("go.mod", files[path.Join(member, "go.mod")].data, nil)
				if memberErr != nil {
					return nil, memberErr
				}
				for _, replacement := range memberMod.Replace {
					if replacement.New.Version == "" {
						if moduleErr := checkModule(member, replacement.New.Path); moduleErr != nil {
							return nil, moduleErr
						}
					}
				}
			}
			for _, replace := range work.Replace {
				if replace.New.Version == "" {
					if moduleErr := checkModule(parent, replace.New.Path); moduleErr != nil {
						return nil, moduleErr
					}
				}
			}
			break
		}
		if parent == "." {
			break
		}
	}
	if mod.Toolchain != nil {
		toolchain := strings.TrimPrefix(mod.Toolchain.Name, "go")
		if !goVersionPattern.MatchString(toolchain) {
			return nil, errors.New("the Go toolchain needs an explicit Dockerfile")
		}
		if version.Compare("go"+toolchain, "go"+goVersion) > 0 {
			goVersion = toolchain
		}
	}
	if !goVersionPattern.MatchString(goVersion) {
		return nil, errors.New("the Go workspace needs a stable Go version or an explicit Dockerfile")
	}
	parts := strings.Split(goVersion, ".")
	moduleRelative, _ := withinBuildContext(path.Join(module, "go.mod"), context)
	moduleRelative = path.Dir(moduleRelative)
	entry, _ := strings.CutPrefix(source, module+"/")
	if source == module {
		entry = "."
	} else if module == "." {
		entry = source
	}
	command, marshalErr := json.Marshal([]string{"go", "build", "-trimpath", "-o", "/out/service", "./" + strings.TrimPrefix(entry, "./")})
	if marshalErr != nil {
		return nil, marshalErr
	}
	workdir := path.Join("/src", moduleRelative)
	if strings.ContainsAny(workdir, "\r\n\x00$") {
		return nil, errors.New("unsupported Go module directory")
	}
	return fmt.Appendf(nil, "FROM golang:%s.%s AS build\nENV CGO_ENABLED=0\nWORKDIR /src\nCOPY . .\nWORKDIR %s\nRUN %s\nFROM gcr.io/distroless/static-debian12:nonroot\nCOPY --from=build /out/service /service\nUSER 65532:65532\nENTRYPOINT [\"/service\"]\n", parts[0], parts[1], workdir, command), nil
}
