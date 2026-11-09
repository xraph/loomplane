package agentknowledge

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
)

// DocsBaseURL is where the published docs live. A page's URL is this plus
// its path.
const DocsBaseURL = "https://www.loomplane.com/docs"

// bundleVersion is the shape of docs.json. Bump it when DocPage changes.
const bundleVersion = 1

// DocPage is one documentation page, reduced to markdown.
type DocPage struct {
	Path        string `json:"path"` // "agents/mcp-server"; "" for the root index
	Title       string `json:"title"`
	Description string `json:"description"`
	URL         string `json:"url"`  // DocsBaseURL + "/" + Path, or DocsBaseURL for the root
	Text        string `json:"text"` // markdown without frontmatter, imports or component tags
}

// Bundle is every docs page, in one file the binary carries.
type Bundle struct {
	Version int       `json:"version"`
	Digest  string    `json:"digest"` // hex sha256 of the encoded pages
	Pages   []DocPage `json:"pages"`  // sorted by Path
}

// docsJSON is the bundle that "make docs-bundle" writes. The test
// TestEmbeddedBundleIsCurrent fails when apps/docs/content moves on
// without it.
//
//go:embed docs.json
var docsJSON []byte

//go:generate go run ./gendocs -root ../../apps/docs/content/docs -out docs.json

// EmbeddedDocs decodes the docs bundle compiled into the binary.
func EmbeddedDocs() (Bundle, error) {
	var b Bundle
	if err := json.Unmarshal(docsJSON, &b); err != nil {
		return Bundle{}, fmt.Errorf("agentknowledge: decode embedded docs: %w", err)
	}
	return b, nil
}

// BuildBundle reads every *.mdx file under root. Other files (meta.json
// among them) are ignored. A page with no frontmatter title is an error
// that names the file.
func BuildBundle(root fs.FS) (Bundle, error) {
	var pages []DocPage
	err := fs.WalkDir(root, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || path.Ext(name) != ".mdx" {
			return nil
		}
		raw, err := fs.ReadFile(root, name)
		if err != nil {
			return err
		}
		meta, body := splitFrontmatter(string(raw))
		if meta["title"] == "" {
			return fmt.Errorf("agentknowledge: %s has no title in its frontmatter", name)
		}
		p := pagePath(name)
		url := DocsBaseURL
		if p != "" {
			url += "/" + p
		}
		pages = append(pages, DocPage{
			Path:        p,
			Title:       meta["title"],
			Description: meta["description"],
			URL:         url,
			Text:        stripMDX(body),
		})
		return nil
	})
	if err != nil {
		return Bundle{}, err
	}
	sort.Slice(pages, func(i, j int) bool { return pages[i].Path < pages[j].Path })
	if pages == nil {
		pages = []DocPage{}
	}
	enc, err := json.Marshal(pages)
	if err != nil {
		return Bundle{}, err
	}
	sum := sha256.Sum256(enc)
	return Bundle{Version: bundleVersion, Digest: hex.EncodeToString(sum[:]), Pages: pages}, nil
}

// EncodeBundle writes the bundle as two-space indented JSON with a
// trailing newline. The same bundle always encodes to the same bytes.
func EncodeBundle(b Bundle) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(b); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// pagePath maps "agents/x.mdx" to "agents/x" and "agents/index.mdx" to
// "agents". The root index is "".
func pagePath(name string) string {
	p := strings.TrimSuffix(name, ".mdx")
	if p == "index" {
		return ""
	}
	return strings.TrimSuffix(p, "/index")
}

// splitFrontmatter reads the leading "---" block as flat key: value pairs
// and returns the rest of the file.
func splitFrontmatter(src string) (meta map[string]string, body string) {
	meta = map[string]string{}
	src = strings.ReplaceAll(src, "\r\n", "\n")
	if !strings.HasPrefix(src, "---\n") {
		return meta, src
	}
	rest := src[len("---\n"):]
	var block string
	if strings.HasPrefix(rest, "---\n") {
		body = rest[len("---\n"):]
	} else if i := strings.Index(rest, "\n---\n"); i >= 0 {
		block, body = rest[:i], rest[i+len("\n---\n"):]
	} else if strings.HasSuffix(rest, "\n---") {
		block = strings.TrimSuffix(rest, "\n---")
	} else {
		return meta, src
	}
	for _, line := range strings.Split(block, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "#") {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		}
		meta[strings.TrimSpace(k)] = v
	}
	return meta, body
}

var (
	importLine = regexp.MustCompile(`^import\s.+\sfrom\s+['"][^'"]+['"];?\s*$`)
	jsxTag     = regexp.MustCompile(`</?[A-Z][A-Za-z0-9]*(?:\s[^<>]*)?/?>`)
	selfClose  = regexp.MustCompile(`^<[A-Z][A-Za-z0-9]*\s[^<>]*/>$`)
	titleAttr  = regexp.MustCompile(`\btitle="([^"]*)"`)
	hrefAttr   = regexp.MustCompile(`\bhref="([^"]*)"`)
)

// stripMDX turns the MDX body into plain markdown: imports and component
// tags go, their inner text stays, fenced code is left byte for byte.
func stripMDX(body string) string {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	var out []string
	blanks := 0
	var fence string // the opening marker while inside a fenced block
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if fence != "" {
			out = append(out, line)
			if strings.HasPrefix(trimmed, fence) && strings.Trim(trimmed, fence[:1]) == "" {
				fence = ""
			}
			continue
		}
		if m := fenceOpen(trimmed); m != "" {
			fence, blanks = m, 0
			out = append(out, line)
			continue
		}
		if importLine.MatchString(line) {
			continue
		}
		if trimmed == "" {
			blanks++
			if blanks <= 2 {
				out = append(out, "")
			}
			continue
		}
		if selfClose.MatchString(trimmed) {
			if t := titleAttr.FindStringSubmatch(trimmed); t != nil {
				item := "- " + t[1]
				if h := hrefAttr.FindStringSubmatch(trimmed); h != nil {
					item += " (" + h[1] + ")"
				}
				blanks = 0
				out = append(out, item)
			}
			continue
		}
		cleaned := stripTags(line)
		if strings.TrimSpace(cleaned) == "" {
			continue // a line that held only tags
		}
		blanks = 0
		out = append(out, cleaned)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// fenceOpen returns the fence marker when the line opens a fenced block.
func fenceOpen(trimmed string) string {
	for _, c := range []string{"`", "~"} {
		n := len(trimmed) - len(strings.TrimLeft(trimmed, c))
		if n >= 3 {
			return strings.Repeat(c, n)
		}
	}
	return ""
}

// stripTags removes component tags from a line, leaving inline code spans
// alone so `<Foo>` written as code survives.
func stripTags(line string) string {
	parts := strings.Split(line, "`")
	for i := 0; i < len(parts); i += 2 {
		parts[i] = jsxTag.ReplaceAllString(parts[i], "")
	}
	return strings.Join(parts, "`")
}
