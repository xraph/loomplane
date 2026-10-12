package detect

import (
	"bytes"
	"context"
	"path"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/xraph/loomplane/pkg/discovery"
)

// Docker finds Dockerfiles: what each builds from and the ports it exposes.
type Docker struct{}

// Name implements discovery.Detector.
func (Docker) Name() string { return "docker" }

func isDockerfile(name string) bool {
	base := path.Base(name)
	return base == "Dockerfile" || base == "Containerfile" ||
		strings.HasPrefix(base, "Dockerfile.") || strings.HasSuffix(base, ".Dockerfile")
}

// Detect implements discovery.Detector.
func (Docker) Detect(ctx context.Context, repo discovery.Repo, r *discovery.Report) error {
	for _, f := range repo.Files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !isDockerfile(f) {
			continue
		}
		data, n := read(repo, f)
		if n != nil {
			note(r, n)
			continue
		}
		img := discovery.Image{Path: f, Context: dir(f)}
		for _, in := range instructions(data) {
			instr, rest, _ := strings.Cut(in.text, " ")
			switch strings.ToUpper(instr) {
			case "FROM":
				// The last FROM is the stage the image ships.
				for _, fld := range strings.Fields(rest) {
					if !strings.HasPrefix(fld, "--") {
						img.Base = fld
						break
					}
				}
				img.Evidence = replaceEvidence(img.Evidence, "FROM", evidence(data, f, in.line))
			case "EXPOSE":
				for _, p := range strings.Fields(rest) {
					if port, ok := containerPort(p); ok {
						img.Ports = appendPort(img.Ports, port)
					}
				}
				img.Evidence = append(img.Evidence, evidence(data, f, in.line))
			case "RUN":
				if strings.Contains(rest, "build") {
					img.Builds = append(img.Builds, discovery.Evidence{Path: f, Line: in.line, Detail: clip(in.text)})
				}
			}
		}
		r.Images = append(r.Images, img)
	}
	return nil
}

type instruction struct {
	line int
	text string
}

// instructions joins a Dockerfile's continued lines (ending in a
// backslash) into whole instructions, each with the line it starts on.
// Comments and blank lines inside a continuation are dropped, as Docker
// drops them.
func instructions(data []byte) []instruction {
	var out []instruction
	var cur *instruction
	for i, raw := range bytes.Split(data, []byte("\n")) {
		line := strings.TrimSpace(string(raw))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		cont := strings.HasSuffix(line, "\\")
		line = strings.TrimSpace(strings.TrimSuffix(line, "\\"))
		if cur == nil {
			cur = &instruction{line: i + 1, text: line}
		} else {
			cur.text += " " + line
		}
		if !cont {
			out = append(out, *cur)
			cur = nil
		}
	}
	if cur != nil {
		out = append(out, *cur)
	}
	return out
}

// maxDetail caps evidence text. It's generous because a build step's
// target often comes last: `go build ... -o /out/api ./cmd/api`.
const maxDetail = 600

// clip shortens long evidence text, such as a RUN instruction joined from
// many lines.
func clip(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= maxDetail {
		return s
	}
	return s[:maxDetail] + "…"
}

// replaceEvidence keeps one entry whose detail starts with prefix: the last.
func replaceEvidence(list []discovery.Evidence, prefix string, ev discovery.Evidence) []discovery.Evidence {
	out := list[:0]
	for _, e := range list {
		if !strings.HasPrefix(strings.ToUpper(e.Detail), prefix) {
			out = append(out, e)
		}
	}
	return append(out, ev)
}

// containerPort reads the container side of "8080", "8080/tcp",
// "3000:8080" or "127.0.0.1:3000:8080".
func containerPort(s string) (int, bool) {
	s, _, _ = strings.Cut(s, "/")
	if i := strings.LastIndex(s, ":"); i >= 0 {
		s = s[i+1:]
	}
	p, err := strconv.Atoi(s)
	if err != nil || p <= 0 || p > 65535 {
		return 0, false
	}
	return p, true
}

func appendPort(ports []int, p int) []int {
	for _, x := range ports {
		if x == p {
			return ports
		}
	}
	ports = append(ports, p)
	sort.Ints(ports)
	return ports
}

// Compose finds Compose files: the services they build from this
// repository, and the backing services they run from public images.
type Compose struct{}

// Name implements discovery.Detector.
func (Compose) Name() string { return "compose" }

func isComposeFile(name string) bool {
	base := path.Base(name)
	if ext := path.Ext(base); ext != ".yml" && ext != ".yaml" {
		return false
	}
	return strings.HasPrefix(base, "docker-compose") || strings.HasPrefix(base, "compose.") || strings.HasPrefix(base, "compose-")
}

// composeImages maps an image's name, without registry, owner or tag, to
// the backing service it runs.
var composeImages = map[string]string{
	"postgres":          "postgres",
	"postgresql":        "postgres",
	"postgis":           "postgres",
	"timescaledb":       "postgres",
	"timescaledb-ha":    "postgres",
	"redis":             "redis",
	"redis-stack":       "redis",
	"valkey":            "redis",
	"keydb":             "redis",
	"mysql":             "mysql",
	"mariadb":           "mysql",
	"mongo":             "mongodb",
	"mongodb":           "mongodb",
	"rabbitmq":          "rabbitmq",
	"kafka":             "kafka",
	"cp-kafka":          "kafka",
	"redpanda":          "kafka",
	"nats":              "nats",
	"minio":             "object-storage",
	"elasticsearch":     "search",
	"opensearch":        "search",
	"memcached":         "memcached",
	"clickhouse-server": "clickhouse",
}

func imageName(ref string) string {
	ref, _, _ = strings.Cut(ref, "@")
	base := path.Base(ref)
	base, _, _ = strings.Cut(base, ":")
	return base
}

// Detect implements discovery.Detector.
func (Compose) Detect(ctx context.Context, repo discovery.Repo, r *discovery.Report) error {
	for _, f := range repo.Files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !isComposeFile(f) {
			continue
		}
		data, n := read(repo, f)
		if n != nil {
			note(r, n)
			continue
		}
		var doc yaml.Node
		if err := yaml.Unmarshal(data, &doc); err != nil || len(doc.Content) == 0 {
			note(r, &discovery.Note{Path: f, Message: "not a valid Compose file"})
			continue
		}
		services := yamlKey(doc.Content[0], "services")
		if services == nil || services.Kind != yaml.MappingNode {
			continue
		}
		for i := 0; i+1 < len(services.Content); i += 2 {
			composeService(r, f, data, services.Content[i+1])
		}
	}
	return nil
}

func composeService(r *discovery.Report, file string, data []byte, svc *yaml.Node) {
	var ports []int
	var portEv []discovery.Evidence
	if seq := yamlKey(svc, "ports"); seq != nil && seq.Kind == yaml.SequenceNode {
		for _, item := range seq.Content {
			target := item.Value
			if item.Kind == yaml.MappingNode {
				if t := yamlKey(item, "target"); t != nil {
					target = t.Value
				}
			}
			if p, ok := containerPort(target); ok {
				ports = appendPort(ports, p)
				portEv = append(portEv, evidence(data, file, item.Line))
			}
		}
	}

	if build := yamlKey(svc, "build"); build != nil {
		ctxDir, dockerfile := build.Value, "Dockerfile"
		if build.Kind == yaml.MappingNode {
			ctxDir = ""
			if c := yamlKey(build, "context"); c != nil {
				ctxDir = c.Value
			}
			if d := yamlKey(build, "dockerfile"); d != nil {
				dockerfile = d.Value
			}
		}
		if ctxDir == "" {
			ctxDir = "."
		}
		if strings.Contains(ctxDir, "://") {
			return // a remote build context isn't in this repository
		}
		buildCtx := join(dir(file), ctxDir)
		img := discovery.Image{Path: join(buildCtx, dockerfile), Context: buildCtx, Ports: ports,
			Evidence: append([]discovery.Evidence{evidence(data, file, build.Line)}, portEv...)}
		mergeImage(r, img)
		return
	}

	if image := yamlKey(svc, "image"); image != nil {
		if kind, ok := composeImages[imageName(image.Value)]; ok {
			r.Resources = append(r.Resources, discovery.ResourceUse{
				Kind: kind, Confidence: discovery.Likely, Evidence: evidence(data, file, image.Line),
			})
		}
	}
}

// mergeImage adds what a Compose file says about a Dockerfile to what the
// Dockerfile itself says.
func mergeImage(r *discovery.Report, img discovery.Image) {
	for i := range r.Images {
		if r.Images[i].Path == img.Path {
			r.Images[i].Context = img.Context
			for _, p := range img.Ports {
				r.Images[i].Ports = appendPort(r.Images[i].Ports, p)
			}
			r.Images[i].Evidence = append(r.Images[i].Evidence, img.Evidence...)
			return
		}
	}
	r.Images = append(r.Images, img)
}
