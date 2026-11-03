package fencerules

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestJournalKeyMatchesPython(t *testing.T) {
	// python3 -c "import hashlib;print(hashlib.sha256('org/op'.encode()).hexdigest())"
	const python = "447d62b33a7d1062bf5c9af48c4da01e23ab748f9b1ed4e75dce3658d34113bd"
	if got := JournalKey("org/op"); got != python {
		t.Fatalf("JournalKey = %s, want %s", got, python)
	}
	sum := sha256.Sum256([]byte("org/op"))
	if hex.EncodeToString(sum[:]) != python {
		t.Fatal("literal is not sha256 of the operation")
	}
	j := Journal{}
	if err := Advance(&j, "org/op", 4); err != nil {
		t.Fatal(err)
	}
	if j.Epochs[python] != 4 {
		t.Fatalf("epochs = %v, want key %s", j.Epochs, python)
	}
}

func TestAdvanceRefusesOlder(t *testing.T) {
	tests := []struct {
		name    string
		journal Journal
		gen     int64
		wantErr bool
		want    int64
	}{
		{"first claim", Journal{}, 1, false, 1},
		{"newer", Journal{Epochs: map[string]int64{JournalKey("o"): 2}}, 3, false, 3},
		{"same", Journal{Epochs: map[string]int64{JournalKey("o"): 2}}, 2, false, 2},
		{"older", Journal{Epochs: map[string]int64{JournalKey("o"): 2}}, 1, true, 2},
		{"dirty", Journal{Epochs: map[string]int64{}, Dirty: true}, 1, true, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := Advance(&tc.journal, "o", tc.gen)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Advance err = %v, wantErr %v", err, tc.wantErr)
			}
			if got := tc.journal.Epochs[JournalKey("o")]; got != tc.want {
				t.Fatalf("epoch = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestAdmitRequiresCurrent(t *testing.T) {
	j := Journal{Epochs: map[string]int64{JournalKey("o"): 2}}
	tests := []struct {
		name    string
		journal Journal
		op      string
		gen     int64
		wantErr bool
	}{
		{"current", j, "o", 2, false},
		{"older", j, "o", 1, true},
		{"newer", j, "o", 3, true},
		{"never advanced", j, "other", 1, true},
		{"zero current", Journal{}, "o", 0, true},
		{"dirty", Journal{Epochs: j.Epochs, Dirty: true}, "o", 2, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := Admit(tc.journal, tc.op, tc.gen); (err != nil) != tc.wantErr {
				t.Fatalf("Admit err = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

const (
	execID      = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	startPath   = "/exec/" + execID + "/start"
	startBody   = `{"Detach":false,"Tty":false}`
	containerID = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
)

func okExec() ExecInspect {
	return ExecInspect{ContainerID: containerID, Entrypoint: "sh", Args: []string{"-c", "x"}}
}

func TestCheckExecRejectsTTY(t *testing.T) {
	e := okExec()
	e.TTY = true
	if err := CheckExec("exec", "POST", startPath, []byte(startBody), e); err == nil {
		t.Fatal("a TTY exec was admitted")
	}
	e = okExec()
	e.Privileged = true
	if err := CheckExec("exec", "POST", startPath, []byte(startBody), e); err == nil {
		t.Fatal("a privileged exec was admitted")
	}
}

func TestCheckExecRejectsRunning(t *testing.T) {
	e := okExec()
	e.Running = true
	if err := CheckExec("ingress-exec", "POST", startPath, []byte(startBody), e); err == nil {
		t.Fatal("a running exec was admitted")
	}
}

func TestCheckExecRows(t *testing.T) {
	badContainer := okExec()
	badContainer.ContainerID = "short"
	tests := []struct {
		name         string
		mode, method string
		path, body   string
		exec         ExecInspect
		wantErr      bool
	}{
		{"exec ok", "exec", "POST", startPath, startBody, okExec(), false},
		{"ingress-exec ok", "ingress-exec", "POST", startPath, startBody, okExec(), false},
		{"other mode", "request", "POST", startPath, startBody, okExec(), true},
		{"get", "exec", "GET", startPath, startBody, okExec(), true},
		{"short id", "exec", "POST", "/exec/abc/start", startBody, okExec(), true},
		{"upper id", "exec", "POST", "/exec/" + strings.ToUpper(execID) + "/start", startBody, okExec(), true},
		{"other suffix", "exec", "POST", "/exec/" + execID + "/resize", startBody, okExec(), true},
		{"prefix path", "exec", "POST", "/x" + startPath, startBody, okExec(), true},
		{"detach", "exec", "POST", startPath, `{"Detach":true,"Tty":false}`, okExec(), true},
		{"tty body", "exec", "POST", startPath, `{"Detach":false,"Tty":true}`, okExec(), true},
		{"extra field", "exec", "POST", startPath, `{"Detach":false,"Tty":false,"User":"root"}`, okExec(), true},
		{"empty body", "exec", "POST", startPath, ``, okExec(), true},
		{"bad body", "exec", "POST", startPath, `nope`, okExec(), true},
		{"bad container id", "exec", "POST", startPath, startBody, badContainer, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckExec(tc.mode, tc.method, tc.path, []byte(tc.body), tc.exec)
			if (err != nil) != tc.wantErr {
				t.Fatalf("CheckExec err = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func ingressExec(args ...string) ExecInspect {
	e := okExec()
	e.Args = args
	return e
}

func ownedProxy() ContainerInspect {
	return ContainerInspect{
		ID: containerID, Image: LegacyIngressImage, Running: true,
		Labels: map[string]string{"loomplane.org": "org", "loomplane.ingress": "srv"},
	}
}

func TestCheckIngressExecAllowedForms(t *testing.T) {
	tests := []struct {
		name string
		exec ExecInspect
	}{
		{"read", ingressExec("-c", IngressAdminScript, "ingress", "read", "", "")},
		{"apply", ingressExec("-c", IngressAdminScript, "ingress", "apply", "etag", "Y29uZmln")},
		{"reload", ingressExec("-c", IngressAdminScript, "ingress", "reload", "etag", "Y29uZmln")},
		{"probe", ingressExec("-c", IngressProbeScript, "probe", "app-1.example.com")},
		{"dns", ingressExec("-c", IngressTokenScript, "dns", "zone_1")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := CheckIngressExec(tc.exec, ownedProxy(), "org/ingress/srv"); err != nil {
				t.Fatalf("CheckIngressExec: %v", err)
			}
		})
	}
}

func TestCheckIngressExecRejectsForms(t *testing.T) {
	notSh := ingressExec("-c", IngressProbeScript, "probe", "a.example.com")
	notSh.Entrypoint = "bash"
	tests := []struct {
		name string
		exec ExecInspect
	}{
		{"entrypoint", notSh},
		{"no args", ingressExec()},
		{"not -c", ingressExec("-x", IngressProbeScript, "probe", "a.example.com")},
		{"other script", ingressExec("-c", "rm -rf /", "probe", "a.example.com")},
		{"admin bad action", ingressExec("-c", IngressAdminScript, "ingress", "delete", "", "")},
		{"admin wrong label arg", ingressExec("-c", IngressAdminScript, "other", "read", "", "")},
		{"admin short", ingressExec("-c", IngressAdminScript, "ingress", "read", "")},
		{"probe uppercase host", ingressExec("-c", IngressProbeScript, "probe", "A.example.com")},
		{"probe shell host", ingressExec("-c", IngressProbeScript, "probe", "a;b")},
		{"probe wrong label", ingressExec("-c", IngressProbeScript, "dns", "a.example.com")},
		{"dns traversal", ingressExec("-c", IngressTokenScript, "dns", "../zone")},
		{"dns too long", ingressExec("-c", IngressTokenScript, "dns", strings.Repeat("a", 65))},
		{"dns empty", ingressExec("-c", IngressTokenScript, "dns", "")},
		{"probe script with dns label", ingressExec("-c", IngressProbeScript, "dns", "zone_1")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := CheckIngressExec(tc.exec, ownedProxy(), "org/ingress/srv"); err == nil {
				t.Fatal("admitted")
			}
		})
	}
}

func TestCheckIngressExecRejectsOtherImage(t *testing.T) {
	good := ingressExec("-c", IngressProbeScript, "probe", "a.example.com")
	other := ownedProxy()
	other.Image = "alpine:latest"
	if err := CheckIngressExec(good, other, "org/ingress/srv"); err == nil {
		t.Fatal("an unlisted image was admitted")
	}
	// The DNS image is allowed only once it is pinned.
	other.Image = "ghcr.io/example/dns@sha256:1"
	if err := CheckIngressExec(good, other, "org/ingress/srv"); err == nil {
		t.Fatal("an unpinned DNS image was admitted")
	}
	previous := DNSImage
	DNSImage = other.Image
	t.Cleanup(func() { DNSImage = previous })
	if err := CheckIngressExec(good, other, "org/ingress/srv"); err != nil {
		t.Fatalf("pinned DNS image: %v", err)
	}
}

func TestCheckIngressExecRejectsOperationMismatch(t *testing.T) {
	good := ingressExec("-c", IngressProbeScript, "probe", "a.example.com")
	tests := []struct {
		name      string
		owned     func(*ContainerInspect)
		operation string
	}{
		{"different ingress", nil, "org/ingress/other"},
		{"different org", nil, "elsewhere/ingress/srv"},
		{"bare org", nil, "org"},
		{"no org label", func(c *ContainerInspect) { delete(c.Labels, "loomplane.org") }, "/ingress/srv"},
		{"no ingress label", func(c *ContainerInspect) { delete(c.Labels, "loomplane.ingress") }, "org/ingress/"},
		{"not running", func(c *ContainerInspect) { c.Running = false }, "org/ingress/srv"},
		{"other container", func(c *ContainerInspect) { c.ID = strings.Repeat("a", 64) }, "org/ingress/srv"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := ownedProxy()
			if tc.owned != nil {
				tc.owned(&o)
			}
			if err := CheckIngressExec(good, o, tc.operation); err == nil {
				t.Fatal("admitted")
			}
		})
	}
}
