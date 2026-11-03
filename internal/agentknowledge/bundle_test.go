package agentknowledge

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"testing/fstest"
)

const sampleDoc = "---\ntitle: X\ndescription: D\n---\n\nimport { Step, Steps } from 'fumadocs-ui/components/steps';\n\n" +
	"<Callout type=\"warn\">Careful</Callout>\n\n\n\n" +
	"<Steps>\n<Step>\nOne\n</Step>\n</Steps>\n\n" +
	"```tsx\nimport { A } from 'a';\n<Foo />\n\n\n\nend\n```\n\n" +
	"<Card title=\"AWS\" href=\"/docs/clouds/aws\" />\n"

func TestBuildBundleStripsMDX(t *testing.T) {
	b, err := BuildBundle(fstest.MapFS{"agents/x.mdx": {Data: []byte(sampleDoc)}})
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Pages) != 1 {
		t.Fatalf("pages = %d, want 1", len(b.Pages))
	}
	p := b.Pages[0]
	if p.Title != "X" || p.Description != "D" || p.URL != "https://www.loomplane.com/docs/agents/x" {
		t.Fatalf("page = %+v", p)
	}
	for _, want := range []string{"Careful", "One", "- AWS (/docs/clouds/aws)", "```tsx\nimport { A } from 'a';\n<Foo />\n\n\n\nend\n```"} {
		if !strings.Contains(p.Text, want) {
			t.Errorf("text missing %q:\n%s", want, p.Text)
		}
	}
	// Only the fenced block may keep "import" and a tag.
	outside := strings.Replace(p.Text, "```tsx\nimport { A } from 'a';\n<Foo />\n\n\n\nend\n```", "FENCE", 1)
	for _, bad := range []string{"import", "<Callout", "<Step", "</Callout", "<Card"} {
		if strings.Contains(outside, bad) {
			t.Errorf("text still has %q:\n%s", bad, p.Text)
		}
	}
	if strings.Contains(p.Text, "title:") || strings.Contains(outside, "\n\n\n\n") {
		t.Errorf("frontmatter or blank runs left:\n%s", p.Text)
	}
}

func TestBuildBundlePaths(t *testing.T) {
	doc := func(title string) *fstest.MapFile {
		return &fstest.MapFile{Data: []byte("---\ntitle: " + title + "\n---\nbody\n")}
	}
	b, err := BuildBundle(fstest.MapFS{
		"index.mdx":        doc("Home"),
		"agents/index.mdx": doc("Agents"),
		"agents/meta.json": {Data: []byte(`{"title":"Agents"}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Pages) != 2 {
		t.Fatalf("pages = %+v", b.Pages)
	}
	if b.Pages[0].Path != "" || b.Pages[0].URL != DocsBaseURL {
		t.Errorf("root = %+v", b.Pages[0])
	}
	if b.Pages[1].Path != "agents" || b.Pages[1].URL != DocsBaseURL+"/agents" {
		t.Errorf("agents = %+v", b.Pages[1])
	}
}

func TestBuildBundleIsDeterministic(t *testing.T) {
	fsys := fstest.MapFS{
		"z.mdx":      {Data: []byte("---\ntitle: Z\n---\nz\n")},
		"a/b.mdx":    {Data: []byte("---\ntitle: B\n---\nb\n")},
		"a/index.md": {Data: []byte("ignored")},
	}
	b1, err := BuildBundle(fsys)
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := BuildBundle(fsys)
	e1, err := EncodeBundle(b1)
	if err != nil {
		t.Fatal(err)
	}
	e2, _ := EncodeBundle(b2)
	if !bytes.Equal(e1, e2) || b1.Digest == "" || b1.Digest != b2.Digest {
		t.Fatal("bundle is not deterministic")
	}
	if b1.Version != 1 || b1.Pages[0].Path != "a/b" || b1.Pages[1].Path != "z" {
		t.Fatalf("order/version: %+v", b1)
	}
	if !bytes.HasSuffix(e1, []byte("}\n")) {
		t.Error("no trailing newline")
	}
}

func TestBuildBundleNeedsTitles(t *testing.T) {
	_, err := BuildBundle(fstest.MapFS{"agents/x.mdx": {Data: []byte("---\ndescription: D\n---\nbody\n")}})
	if err == nil || !strings.Contains(err.Error(), "agents/x.mdx") {
		t.Fatalf("err = %v", err)
	}
}

func TestEmbeddedBundleIsCurrent(t *testing.T) {
	b, err := BuildBundle(os.DirFS("../../apps/docs/content/docs"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := EncodeBundle(b)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, docsJSON) {
		t.Fatal("apps/docs/content changed; run make docs-bundle and commit internal/agentknowledge/docs.json")
	}
	if _, err := EmbeddedDocs(); err != nil {
		t.Fatal(err)
	}
}
