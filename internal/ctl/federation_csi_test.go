package ctl

import (
	"bytes"
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestCSIManifestNeedsNoLoomplaneCredentials(t *testing.T) {
	for _, args := range [][]string{
		{"federation", "csi-manifest", "--namespace", "spire"},
		{"federation", "csi-manifest", "--namespace", "../bad"},
		{"federation", "csi-manifest", "--namespace", "spire", "unexpected"},
		{"federation", "csi-manifest"},
	} {
		var out bytes.Buffer
		app := New(Options{Out: &out, ConfigPath: t.TempDir() + "/missing", Getenv: func(string) string { return "" }})
		err := app.Run(args)
		if len(args) == 4 && args[3] == "spire" {
			if err != nil {
				t.Fatal(err)
			}
			var list corev1.List
			if err = json.Unmarshal(out.Bytes(), &list); err != nil || len(list.Items) != 3 {
				t.Fatal("invalid manifest", err, out.String())
			}
		} else if err == nil {
			t.Fatal("invalid invocation accepted", args)
		}
	}
}
