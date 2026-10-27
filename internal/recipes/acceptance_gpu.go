//go:build acceptance

package recipes

// AcceptanceGPUKind is the test-only GPU kind the live GPU qualification
// deploys. It's registered only in binaries built with the acceptance tag,
// so the product's catalog never lists it.
const AcceptanceGPUKind = "lpaccept-gpu"

// acceptanceGPUServe prints what nvidia-smi sees, then answers every HTTP
// request on :8000 with 200. The CUDA base image has no Python, so the
// server is Perl from the Ubuntu base. It reads each request's headers
// before replying, so a probe never sees a reset.
const acceptanceGPUServe = `nvidia-smi && exec perl -MIO::Socket::INET -e '$| = 1; ` +
	`my $s = IO::Socket::INET->new(LocalPort => 8000, Listen => 16, ReuseAddr => 1) or die "listen on 8000: $!\n"; ` +
	`print "LPACCEPT_GPU_SERVING 8000\n"; ` +
	`while (my $c = $s->accept) { while (my $l = <$c>) { last if $l =~ /^\r?$/ } ` +
	`print $c "HTTP/1.0 200 OK\r\nContent-Type: text/plain\r\nContent-Length: 3\r\n\r\nok\n"; close $c }'`

func init() {
	MustRegister(Kind{
		Name:        AcceptanceGPUKind,
		Title:       "Acceptance GPU",
		Description: "A test-only kind: one process on one NVIDIA GPU that prints nvidia-smi and answers HTTP on port 8000.",
		Placement:   PlacementAppProject,
		Targets:     []string{"server", "kubernetes"},
		Releases: []Release{{
			Version: "1", Product: "12.2.2", Revision: "1",
			// nvidia/cuda:12.2.2-base-ubuntu22.04, amd64 only. CUDA 12.2
			// needs an NVIDIA driver of 535 or later on the host.
			Images: map[string]Image{"server": {Repository: "docker.io/nvidia/cuda", Digests: map[string]string{
				"amd64": "sha256:db91f04084e04eee8670cfb732fe6396010cad7493e45af3b6ad979be590a15d",
			}}},
		}},
		Components: []Component{{
			Name:      "server",
			Role:      RoleProcess,
			Command:   []string{"sh", "-c", acceptanceGPUServe},
			Ports:     []Port{{Name: "http", Container: 8000, Protocol: "http"}},
			Readiness: &HTTPProbe{Port: "http", Path: "/"},
			Resources: Resources{
				CPUMillis: 500, MemoryMB: 2048,
				GPU: &GPU{Count: 1}, ShmMB: 1024,
			},
			WeightCache: &WeightCache{MountPath: "/cache", KubernetesSizeMB: 1024},
		}},
		Outputs: []Output{{Name: "url", EnvKey: "LPACCEPT_GPU_URL", From: &Ref{Component: "server", Field: "url:http"}}},
	})
}
