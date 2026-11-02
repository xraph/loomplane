package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/xraph/ctrlplane/id"
	cpprovider "github.com/xraph/ctrlplane/provider"

	"github.com/xraph/loomplane/internal/clouds/owned"
)

func TestRuntime_replacesContainerStoppedByInterruptedAttempt(t *testing.T) {
	iid := id.New(id.PrefixInstance)
	labels := map[string]string{owned.LabelOrg: "org", owned.LabelProject: "project", owned.LabelEnvironment: "env", owned.LabelInstance: iid.String(), owned.LabelRevision: "new"}
	name := ContainerName(iid)
	oldLabels := map[string]string{owned.LabelOrg: "org", owned.LabelProject: "project", owned.LabelEnvironment: "env", owned.LabelInstance: iid.String(), owned.LabelRevision: "old"}
	old := &containerInfo{ID: "old"}
	old.Config.Image, old.Config.Labels = "sha256:old", oldLabels
	old.State.Status = "exited"
	containers := map[string]*containerInfo{name: old}
	var mutations []string
	c := &Client{Config: &Config{Details: map[string]string{"docker_id": "fixture"}}, HTTP: &http.Client{Transport: runtimeTransport(func(req *http.Request) (*http.Response, error) {
		status, body := http.StatusOK, ""
		if req.Method != http.MethodGet {
			mutations = append(mutations, req.Method+" "+req.URL.RequestURI())
		}
		switch {
		case req.Method == http.MethodGet && req.URL.Path == "/networks/"+NetworkName("org", "env"):
			body = `{"Labels":{"loomplane.org":"org","loomplane.environment":"env"}}`
		case req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/json"):
			key := strings.TrimSuffix(strings.TrimPrefix(req.URL.Path, "/containers/"), "/json")
			if info := containers[key]; info != nil {
				data, err := json.Marshal(info)
				if err != nil {
					t.Fatal(err)
				}
				body = string(data)
			} else {
				status = http.StatusNotFound
			}
		case req.Method == http.MethodPost && req.URL.RequestURI() == "/containers/old/stop?t=10":
			status = http.StatusNotModified
		case req.Method == http.MethodPost && req.URL.Path == "/containers/old/rename":
			delete(containers, name)
			containers[req.URL.Query().Get("name")] = old
			status = http.StatusNoContent
		case req.Method == http.MethodPost && req.URL.Path == "/containers/create":
			candidate := &containerInfo{ID: "candidate"}
			candidate.Config.Image, candidate.Config.Labels = "sha256:new", labels
			containers[name] = candidate
			status, body = http.StatusCreated, `{"Id":"candidate"}`
		case req.Method == http.MethodPost && req.URL.Path == "/containers/candidate/start":
			containers[name].State.Running = true
			status = http.StatusNoContent
		case req.Method == http.MethodDelete && req.URL.Path == "/containers/old":
			delete(containers, name+"-previous-stopped")
			status = http.StatusNoContent
		default:
			t.Errorf("unexpected request: %s %s", req.Method, req.URL)
			status = http.StatusInternalServerError
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})}}
	runtime := &Runtime{Session: func(context.Context, map[string]string) (*Client, error) { return c, nil }}
	result, err := runtime.Apply(t.Context(), iid, cpprovider.ServiceSpec{Name: "main", Image: "sha256:new"}, labels)
	if err != nil {
		t.Fatal(err)
	}
	if result.ServiceRefs["main"] != "candidate" || len(containers) != 1 || !containers[name].State.Running {
		t.Fatalf("replacement did not converge: %+v %+v", result, containers)
	}
	want := []string{"POST /containers/old/stop?t=10", "POST /containers/old/rename?name=" + name + "-previous-stopped", "POST /containers/create?name=" + name, "POST /containers/candidate/start", "DELETE /containers/old?force=1&v=0"}
	if !reflect.DeepEqual(mutations, want) {
		t.Fatalf("replacement mutations: got %v, want %v", mutations, want)
	}
}

func TestRuntime_setRunningAcceptsOnlyDocumentedIdempotentResponses(t *testing.T) {
	for _, running := range []bool{false, true} {
		action := "stop?t=10"
		if running {
			action = "start"
		}
		for _, status := range []int{http.StatusNoContent, http.StatusNotModified, http.StatusBadRequest, http.StatusNotFound, http.StatusInternalServerError} {
			t.Run(action+"/"+http.StatusText(status), func(t *testing.T) {
				iid := id.New(id.PrefixInstance)
				labels := map[string]string{owned.LabelOrg: "org", owned.LabelProject: "project", owned.LabelEnvironment: "env", owned.LabelInstance: iid.String()}
				body := &runtimeResponseBody{Reader: strings.NewReader(`{"message":"daemon failure"}`)}
				c := &Client{HTTP: &http.Client{Transport: runtimeTransport(func(req *http.Request) (*http.Response, error) {
					if req.Method == http.MethodGet && req.URL.Path == "/containers/"+ContainerName(iid)+"/json" {
						info := &containerInfo{ID: "exact-owned-id"}
						info.Config.Labels = labels
						data, _ := json.Marshal(info)
						return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(data)))}, nil
					}
					if req.Method != http.MethodPost || req.URL.RequestURI() != "/containers/exact-owned-id/"+action {
						t.Errorf("unexpected state control: %s %s", req.Method, req.URL)
					}
					return &http.Response{StatusCode: status, Body: body}, nil
				})}}
				runtime := &Runtime{Session: func(context.Context, map[string]string) (*Client, error) { return c, nil }}
				err := runtime.SetRunning(t.Context(), iid, labels, running)
				if status == http.StatusNoContent || status == http.StatusNotModified {
					if err != nil {
						t.Fatalf("idempotent state control: %v", err)
					}
				} else if err == nil || (status == http.StatusNotFound && !errors.Is(err, ErrMissing)) {
					t.Fatalf("Docker error lost: status %d, error %v", status, err)
				}
				if !body.closed {
					t.Fatal("state response body was not closed")
				}
			})
		}
	}
}

func TestDockerJSON_rejectsNotModifiedForOtherOperations(t *testing.T) {
	for _, operation := range []struct{ method, path string }{
		{http.MethodGet, "/containers/owned/json"},
		{http.MethodPost, "/containers/owned/restart"},
		{http.MethodPost, "/containers/owned/rename?name=new"},
		{http.MethodPost, "/containers/create?name=new"},
		{http.MethodDelete, "/containers/owned"},
	} {
		t.Run(operation.method+operation.path, func(t *testing.T) {
			body := &runtimeResponseBody{Reader: strings.NewReader("")}
			c := &Client{HTTP: &http.Client{Transport: runtimeTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusNotModified, Body: body}, nil
			})}}
			if err := dockerJSON(t.Context(), c, operation.method, operation.path, nil, nil); err == nil || !strings.Contains(err.Error(), "HTTP 304") {
				t.Fatalf("unrelated operation accepted HTTP 304: %v", err)
			}
			if !body.closed {
				t.Fatal("error response body was not closed")
			}
		})
	}
}

type runtimeResponseBody struct {
	io.Reader
	closed bool
}

func (b *runtimeResponseBody) Close() error {
	b.closed = true
	return nil
}
