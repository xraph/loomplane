package detect

import (
	"testing"

	"github.com/xraph/loomplane/pkg/discovery"
)

func TestNode_viteRSCIsAWebAppWithoutIndexHTML(t *testing.T) {
	for _, index := range []bool{false, true} {
		files := map[string]string{
			"package.json":   `{"name":"web","scripts":{"build":"vite build","start":"node server.mjs"},"devDependencies":{"vite":"8","@vitejs/plugin-rsc":"0.5"}}`,
			"vite.config.ts": `import rsc from '@vitejs/plugin-rsc'; export default { plugins: [rsc()] }`,
		}
		if index {
			files["index.html"] = "<html></html>"
		}
		u := onlyUnit(t, run(t, Node{}, files), "node:.")
		if len(u.Frameworks) != 1 || u.Frameworks[0].Name != "vite-rsc" || u.Frameworks[0].Kind != discovery.Web || u.Frameworks[0].Confidence != discovery.Likely || len(u.Entrypoints) != 1 {
			t.Fatalf("RSC app treated as static site or library: %+v", u)
		}
		if u.Commands.Build != "vite build" || u.Commands.Start != "node server.mjs" {
			t.Fatalf("commands lost: %+v", u.Commands)
		}
	}
}

func TestNode_viteRSCDependencyAloneStaysALibrary(t *testing.T) {
	u := onlyUnit(t, run(t, Node{}, map[string]string{"package.json": `{"name":"components","devDependencies":{"@vitejs/plugin-rsc":"0.5","vite":"8"}}`}), "node:.")
	if len(u.Entrypoints) != 0 {
		t.Fatalf("dependency alone became an app: %+v", u)
	}
}
