package server

import (
	"archive/tar"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/xraph/ctrlplane/id"
)

func TestInvalidPostgresPasswordDoesNotOpenTarget(t *testing.T) {
	r := Runtime{Session: func(context.Context, map[string]string) (*Client, error) {
		t.Fatal("resolved credentials for an invalid password")
		return nil, nil
	}}
	if err := r.ConfigurePostgres(t.Context(), id.New(id.PrefixInstance), map[string]string{LabelDatabase: "database"}, "short"); err == nil {
		t.Fatal("invalid password accepted")
	}
}

// A database image that doesn't run as root, such as TimescaleDB's, can read
// and remove an uploaded file only if the archive makes it the owner.
func TestPostgresUploadIsOwnedByTheUserTheToolsRunAs(t *testing.T) {
	iid := id.New(id.PrefixInstance)
	container := ContainerName(iid)
	frame := func(data string) []byte {
		header := make([]byte, 8, 8+len(data))
		header[0] = 1
		binary.BigEndian.PutUint32(header[4:], uint32(len(data)))
		return append(header, data...)
	}
	var (
		commands = map[string][]string{}
		owned    []string
	)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/containers/"+container+"/exec":
			var body struct{ Cmd []string }
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			exec := "exec" + strconv.Itoa(len(commands))
			commands[exec] = body.Cmd
			_ = json.NewEncoder(w).Encode(map[string]string{"Id": exec})
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/exec/") && strings.HasSuffix(r.URL.Path, "/start"):
			cmd := commands[strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/exec/"), "/start")]
			switch strings.Join(cmd, " ") {
			case "id -u":
				_, _ = w.Write(frame("1000\n"))
			case "id -g":
				_, _ = w.Write(frame("1001\n"))
			default:
				t.Errorf("unexpected exec %v", cmd)
			}
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/exec/") && strings.HasSuffix(r.URL.Path, "/json"):
			_, _ = io.WriteString(w, `{"Running":false,"ExitCode":0}`)
		case r.Method == http.MethodPut && r.URL.Path == "/containers/"+container+"/archive":
			archive := tar.NewReader(r.Body)
			for {
				header, err := archive.Next()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Error(err)
					break
				}
				owned = append(owned, fmt.Sprintf("%s %o %d:%d", header.Name, header.Mode, header.Uid, header.Gid))
			}
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
	}))
	defer api.Close()
	u, err := url.Parse(api.URL)
	if err != nil {
		t.Fatal(err)
	}
	c := &Client{Config: &Config{}, HTTP: &http.Client{Transport: runtimeTransport(func(r *http.Request) (*http.Response, error) {
		r.URL.Scheme, r.URL.Host = u.Scheme, u.Host
		return http.DefaultTransport.RoundTrip(r)
	})}}
	db := nativePostgres(c, iid, map[string]string{LabelDatabase: "database"})
	err = db.Upload(t.Context(), "dump", 4, func(w io.Writer) error { _, writeErr := io.WriteString(w, "data"); return writeErr })
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(owned, []string{"dump 600 1000:1001"}) {
		t.Fatalf("uploaded %q", owned)
	}
}
