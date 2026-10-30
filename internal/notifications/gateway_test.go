//go:build integration

package notifications

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	"github.com/xraph/herald/driver/push"

	"github.com/xraph/loomplane/internal/security"
)

// gatewaySend is one request the fake gateway took, with the envelope
// opened by the phone's private key.
type gatewaySend struct {
	Handle string
	Status int
	Body   push.SendBody
	Env    push.Envelope
}

// fakeGateway stands in for the push gateway. It opens each sealed envelope
// with the registered phone's key, the way the app would.
type fakeGateway struct {
	*httptest.Server
	mu    sync.Mutex
	keys  map[string]*ecdh.PrivateKey // by handle
	sends []gatewaySend
	reply map[string][]int // statuses to answer a handle with, in order
	block chan struct{}    // while set, sends wait here until it closes
}

// hang makes the gateway hold every send until the returned function runs.
func (g *fakeGateway) hang() (release func()) {
	g.mu.Lock()
	defer g.mu.Unlock()
	ch := make(chan struct{})
	g.block = ch
	var once sync.Once
	return func() { once.Do(func() { close(ch) }) }
}

func newFakeGateway(t *testing.T) *fakeGateway {
	t.Helper()
	g := &fakeGateway{keys: map[string]*ecdh.PrivateKey{}, reply: map[string][]int{}}
	g.Server = httptest.NewServer(http.HandlerFunc(g.serve))
	t.Cleanup(g.Close)
	return g
}

func (g *fakeGateway) serve(w http.ResponseWriter, r *http.Request) {
	var body push.SendBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || r.URL.Path != "/v1/send" {
		http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
		return
	}
	g.mu.Lock()
	block := g.block
	g.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-r.Context().Done():
			return
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	send := gatewaySend{Handle: body.Handle, Status: http.StatusAccepted, Body: body}
	if queue := g.reply[body.Handle]; len(queue) > 0 {
		send.Status, g.reply[body.Handle] = queue[0], queue[1:]
	}
	if key := g.keys[body.Handle]; key != nil && body.Sealed != "" {
		if sealed, err := base64.StdEncoding.DecodeString(body.Sealed); err == nil {
			if plain, openErr := push.Open(key.Bytes(), sealed); openErr == nil {
				_ = json.Unmarshal(plain, &send.Env)
			}
		}
	}
	g.sends = append(g.sends, send)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(send.Status)
	if send.Status == http.StatusAccepted {
		_, _ = w.Write([]byte(`{"id":"msg"}`))
		return
	}
	_, _ = w.Write([]byte(`{"error":"refused"}`))
}

// answer makes the gateway reply to handle with these statuses, one per send.
func (g *fakeGateway) answer(handle string, statuses ...int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.reply[handle] = statuses
}

func (g *fakeGateway) taken() []gatewaySend {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.sends)
}

// handles lists who each send went to, in order.
func (g *fakeGateway) handles() []string {
	sends := g.taken()
	out := make([]string, 0, len(sends))
	for _, s := range sends {
		out = append(out, s.Handle)
	}
	return out
}

// register adds an Android phone for the user and remembers its key.
func (g *fakeGateway) register(t *testing.T, svc *Service, handle string) PushDevice {
	t.Helper()
	return g.registerFor(t, svc, "user-a", handle)
}

// registerFor adds an Android phone for the named user.
func (g *fakeGateway) registerFor(t *testing.T, svc *Service, user, handle string) PushDevice {
	t.Helper()
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	g.mu.Lock()
	g.keys[handle] = key
	g.mu.Unlock()
	d, err := svc.RegisterDevice(t.Context(), security.Principal{UserID: user, OrgID: "org-a"}, RegisterDeviceInput{
		Handle: handle, PublicKey: base64.StdEncoding.EncodeToString(key.PublicKey().Bytes()), Platform: "android", Name: handle, TimeZone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	// New phones get 22:00 to 07:00 quiet hours. Clear them so what a test
	// sees never depends on the hour it runs; quiet-hours tests set their own.
	d, err = svc.UpdateDevice(t.Context(), security.Principal{UserID: user, OrgID: "org-a"}, d.ID, UpdateDeviceInput{QuietHours: OptionalQuietHours{Set: true}})
	if err != nil {
		t.Fatal(err)
	}
	return d
}
