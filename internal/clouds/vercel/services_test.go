package vercel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestServicesDeploymentConfigCannotOverrideAuthority(t *testing.T) {
	var body map[string]json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("teamId") != "team_approved" {
			t.Error("wrong team")
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		_, _ = w.Write([]byte(`{"id":"dpl_1"}`))
	}))
	defer server.Close()
	var config map[string]json.RawMessage
	if err := json.Unmarshal([]byte(`{"services":{"web":{"root":"web","buildCommand":"custom"}},"rewrites":[{"source":"/(.*)","destination":{"service":"web"}}],"project":"foreign","target":"production","deploymentId":"foreign","gitSource":{"repoId":"foreign"},"env":{"DATABASE_URL":"wrong"},"build":{"env":{"DATABASE_URL":"leaked"}},"projectSettings":{"rootDirectory":"elsewhere"}}`), &config); err != nil {
		t.Fatal(err)
	}
	_, err := New(server.URL).CreateDeploymentWithConfig(t.Context(), Token{Value: "token", TeamID: "team_approved"}, &Project{ID: "prj_approved", Name: "app"}, Preview, nil, nil, map[string]string{"DATABASE_URL": "runtime-only"}, map[string]string{"BUILD_INPUT": "build-value"}, config)
	if err != nil {
		t.Fatal(err)
	}
	if string(body["project"]) != `"prj_approved"` || body["target"] != nil || body["deploymentId"] != nil || body["gitSource"] != nil {
		t.Fatalf("untrusted routing: %s", body)
	}
	if string(body["env"]) != `{"DATABASE_URL":"runtime-only"}` || string(body["build"]) != `{"env":{"BUILD_INPUT":"build-value"}}` {
		t.Fatalf("environment changed: %s", body)
	}
	if body["services"] == nil || body["rewrites"] == nil {
		t.Fatal("configuration was not sent to Vercel")
	}
	var settings map[string]any
	if err := json.Unmarshal(body["projectSettings"], &settings); err != nil {
		t.Fatal(err)
	}
	if settings["rootDirectory"] != nil || settings["sourceFilesOutsideRootDirectory"] != true {
		t.Fatalf("project settings = %v", settings)
	}
}

func TestStandaloneCommandsReachDeploymentProjectSettings(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ProjectSettings map[string]json.RawMessage `json:"projectSettings"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if string(body.ProjectSettings["installCommand"]) != `""` || string(body.ProjectSettings["buildCommand"]) != `"pnpm build"` || string(body.ProjectSettings["commandForIgnoringBuildStep"]) != `"exit 1"` {
			t.Errorf("settings: %s", body.ProjectSettings)
		}
		_, _ = w.Write([]byte(`{"id":"dpl_1"}`))
	}))
	defer server.Close()
	_, err := New(server.URL).CreateDeploymentWithConfig(t.Context(), Token{Value: "token"}, &Project{ID: "prj_1", Name: "web"}, Production, nil, nil, nil, nil, map[string]json.RawMessage{"installCommand": json.RawMessage(`""`), "buildCommand": json.RawMessage(`"pnpm build"`), "ignoreCommand": json.RawMessage(`"exit 1"`)})
	if err != nil {
		t.Fatal(err)
	}
}
