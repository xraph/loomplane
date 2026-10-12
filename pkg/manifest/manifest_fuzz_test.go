package manifest

import "testing"

// FuzzParse checks a manifest anyone can save never crashes the parser,
// and that anything it accepts writes back out and reads in the same.
func FuzzParse(f *testing.F) {
	for _, seed := range []string{
		"version: 1\nservices:\n  api: {source: services/api, provider: cloud-run, needs: [db]}\nresources:\n  db: {kind: postgres}\n",
		"version: 1\nservices:\n  web: {source: apps/web}\ngateways:\n  edge: {routes: [{path: /api, to: web, strip_prefix: true}]}\n",
		"version: 1\ndefaults: {provider: render}\nservices:\n  a: {source: x, needs: [b]}\n  b: {source: y, needs: [a]}\n",
		"version: 2\n",
		"{}",
		"version: 1\nservices:\n  api:\n    source: x\n    environments:\n      preview: {enabled: false, scale: {min: 3, max: 1}}\n",
		"\x00\xff",
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		m, err := Parse(data)
		if err != nil {
			return
		}
		out, err := Marshal(m)
		if err != nil {
			t.Fatalf("a manifest that parsed doesn't marshal: %v", err)
		}
		again, err := Parse(out)
		if err != nil {
			t.Fatalf("what Marshal wrote doesn't parse: %v\n%s", err, out)
		}
		// Equal once written out: an empty map and a missing one are the
		// same manifest.
		if second, _ := Marshal(again); string(second) != string(out) {
			t.Fatalf("the manifest changed on the way through:\n%s\nthen\n%s", out, second)
		}
	})
}
