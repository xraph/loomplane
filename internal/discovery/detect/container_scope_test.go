package detect

import "testing"

func TestDocker_templateInputsAreNotApplicationImages(t *testing.T) {
	r := run(t, Docker{}, map[string]string{
		"internal/generator/program/Dockerfile":  "FROM golang:1.26\nCOPY go.mod main.go ./\nRUN go build -o /app .\nEXPOSE 8080\n",
		"internal/generator/program/go.mod.txt":  "module generated\n",
		"internal/generator/program/main.go.txt": "package main\nfunc main() {}\n",
		"apps/api/Dockerfile":                    "FROM golang:1.26\nCOPY main.go ./\nEXPOSE 8080\n",
		"apps/api/main.go":                       "package main\nfunc main() {}\n",
	})
	if len(r.Images) != 1 || r.Images[0].Path != "apps/api/Dockerfile" {
		t.Fatalf("images = %+v", r.Images)
	}
	if len(r.Notes) != 1 || r.Notes[0].Path != "internal/generator/program/Dockerfile" {
		t.Fatalf("template note missing: %+v", r.Notes)
	}
}
