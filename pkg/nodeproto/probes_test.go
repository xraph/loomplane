package nodeproto

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestFenceFieldsRoundTrip(t *testing.T) {
	in := StreamHeader{
		Endpoint: EndpointDocker,
		Target:   DockerTargetFenced,
		Fence:    &Fence{Resource: "org/op", Generation: 3, Mode: FenceExec, Daemon: "ABCD:EFGH"},
	}
	var buf bytes.Buffer
	if err := WriteHeader(&buf, in); err != nil {
		t.Fatal(err)
	}
	out, err := ReadHeader(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if out.Fence == nil || *out.Fence != *in.Fence || out.Target != DockerTargetFenced {
		t.Fatalf("round trip = %+v", out)
	}
	for _, mode := range []string{FenceAdvance, FenceRequest, FenceExec, FenceIngressExec} {
		if mode == "" {
			t.Fatal("empty mode constant")
		}
	}
}

func TestValidateFencedNeedsFence(t *testing.T) {
	good := func() StreamHeader {
		return StreamHeader{Endpoint: EndpointDocker, Target: DockerTargetFenced,
			Fence: &Fence{Resource: "o/op", Generation: 1, Mode: FenceRequest, Daemon: "d"}}
	}
	if err := good().Validate(); err != nil {
		t.Fatalf("good header: %v", err)
	}
	cases := []struct {
		name string
		edit func(*StreamHeader)
		want string
	}{
		{"no fence", func(h *StreamHeader) { h.Fence = nil }, "fence"},
		{"generation", func(h *StreamHeader) { h.Fence.Generation = 0 }, "generation"},
		{"negative generation", func(h *StreamHeader) { h.Fence.Generation = -2 }, "generation"},
		{"resource", func(h *StreamHeader) { h.Fence.Resource = "" }, "resource"},
		{"daemon", func(h *StreamHeader) { h.Fence.Daemon = "" }, "daemon"},
		{"mode", func(h *StreamHeader) { h.Fence.Mode = "bogus" }, "mode"},
		{"empty mode", func(h *StreamHeader) { h.Fence.Mode = "" }, "mode"},
		{"arg", func(h *StreamHeader) { h.Arg = strings.Repeat("a", MaxArgSize+1) }, "arg"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := good()
			tc.edit(&h)
			err := h.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() = %v, want an error naming %q", err, tc.want)
			}
		})
	}
	// Headers phase 1 produces stay valid.
	for _, h := range []StreamHeader{
		{Endpoint: EndpointProbe},
		{Endpoint: EndpointDocker, Target: DockerTargetSocket},
		{Endpoint: EndpointProbe, Target: ProbeFilesystem, Arg: "/var/lib/docker"},
	} {
		if err := h.Validate(); err != nil {
			t.Fatalf("%+v: %v", h, err)
		}
	}
}

func TestProbeShapesMatchSSH(t *testing.T) {
	keys := func(v any) []string {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		out := make([]string, 0, len(m))
		for k := range m {
			out = append(out, k)
		}
		return out
	}
	same := func(got, want []string) bool {
		if len(got) != len(want) {
			return false
		}
		set := map[string]bool{}
		for _, k := range got {
			set[k] = true
		}
		for _, k := range want {
			if !set[k] {
				return false
			}
		}
		return true
	}
	if k := keys(GPUProbe{}); !same(k, []string{"smi_exit", "smi", "nvidia_pci", "toolkit"}) {
		t.Fatalf("GPUProbe keys = %v", k)
	}
	if k := keys(CgroupLimits{}); !same(k, []string{"cpu", "memory"}) {
		t.Fatalf("CgroupLimits keys = %v", k)
	}
	if k := keys(Filesystem{}); !same(k, []string{"total_bytes", "free_bytes"}) {
		t.Fatalf("Filesystem keys = %v", k)
	}
	if k := keys(Listeners{}); !same(k, []string{"ports"}) {
		t.Fatalf("Listeners keys = %v", k)
	}
}

func TestOldNodeHeaderStillDecodes(t *testing.T) {
	var h StreamHeader
	old := `{"endpoint":"docker","org_id":"o","operation_id":"x","generation":2,"fence":{"resource":"r","generation":2}}`
	if err := json.Unmarshal([]byte(old), &h); err != nil {
		t.Fatal(err)
	}
	if h.Fence == nil || h.Fence.Mode != "" || h.Fence.Daemon != "" || h.Arg != "" || h.Target != "" {
		t.Fatalf("decoded = %+v", h)
	}
}

// The repair modes are an operator's reviewed recovery of a dirty journal.
// They carry no ownership claim, only the daemon they repair.
func TestValidateRepairNeedsOnlyTheDaemon(t *testing.T) {
	for _, mode := range []string{FenceRepairCheck, FenceRepair} {
		h := StreamHeader{Endpoint: EndpointDocker, Target: DockerTargetFenced, Fence: &Fence{Mode: mode, Daemon: "d"}}
		if err := h.Validate(); err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		h.Fence.Daemon = ""
		if err := h.Validate(); err == nil || !strings.Contains(err.Error(), "daemon") {
			t.Fatalf("%s without a daemon: %v", mode, err)
		}
	}
}
