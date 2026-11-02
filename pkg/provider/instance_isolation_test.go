package provider

import "testing"

func TestInstanceIsolationPolicyBounds(t *testing.T) {
	valid := InstanceIsolationPolicy{Revision: InstanceIsolationRevision, PodSecurityVersion: "v1.34", CPUMillis: 2000, MemoryMiB: 1024, EphemeralMiB: 128, MaxPods: 4}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*InstanceIsolationPolicy){
		func(p *InstanceIsolationPolicy) { p.Revision = "unknown" },
		func(p *InstanceIsolationPolicy) { p.PodSecurityVersion = "latest" },
		func(p *InstanceIsolationPolicy) { p.CPUMillis = 0 },
		func(p *InstanceIsolationPolicy) { p.MemoryMiB = -1 },
		func(p *InstanceIsolationPolicy) { p.MaxPods = 0 },
		func(p *InstanceIsolationPolicy) { p.EphemeralMiB = -1 },
		func(p *InstanceIsolationPolicy) { p.IngressPeers = []InstanceNamespacePeer{{}} },
		func(p *InstanceIsolationPolicy) { p.EgressPeers = []InstanceNamespacePeer{{}} },
	} {
		p := valid
		change(&p)
		if err := p.Validate(); err == nil {
			t.Fatal("invalid policy accepted", p)
		}
	}
}

func TestInstanceIsolationEgressNeedsExactHosts(t *testing.T) {
	valid := InstanceIsolationPolicy{Revision: InstanceIsolationRevision, PodSecurityVersion: "v1.34", CPUMillis: 2000, MemoryMiB: 1024, EphemeralMiB: 512, MaxPods: 4,
		Egress: []InstanceNetworkEndpoint{{CIDR: "192.0.2.10/32", Ports: []InstanceNetworkPort{{Protocol: "TCP", Port: 5432}}}}}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, cidr := range []string{"0.0.0.0/0", "192.0.2.0/24", "::/0", "192.0.2.10", "cluster.example.test"} {
		p := valid
		p.Egress = []InstanceNetworkEndpoint{{CIDR: cidr, Ports: valid.Egress[0].Ports}}
		if err := p.Validate(); err == nil {
			t.Fatal("unbounded destination accepted", cidr)
		}
	}
	valid.Egress[0].Ports[0].Port = 0
	if err := valid.Validate(); err == nil {
		t.Fatal("all ports accepted")
	}
}

func TestInstanceLifecycleAccessDoesNotDowngrade(t *testing.T) {
	old := RuntimeAccessProfile{SchemaVersion: 1, CleanupDependents: true}
	jobs := old
	jobs.LifecycleJobs = true
	if !jobs.Includes(old) || old.Includes(jobs) {
		t.Fatal("lifecycle permission inclusion is incorrect")
	}
}

func TestInstanceIsolationRequiresStorageBound(t *testing.T) {
	p := InstanceIsolationPolicy{Revision: InstanceIsolationRevision, PodSecurityVersion: "v1.34", CPUMillis: 2000, MemoryMiB: 1024, MaxPods: 4}
	if err := p.Validate(); err == nil {
		t.Fatal("unbounded container storage accepted")
	}
}

func TestInstancePodStorageAccountsEveryContainer(t *testing.T) {
	group := &PodGroup{RuntimeStorageMiB: 64, Sidecars: []Sidecar{{Name: "helper", Image: "registry.example.test/helper@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Architecture: "arm64", CPUMillis: 250, MemoryMiB: 128, Readiness: ContainerReadiness{Port: 8080}}}, Scratch: []ScratchVolume{{Name: "scratch", SizeMiB: 64}}}
	_, _, storage, err := PodGroupResources(500, 256, group)
	if err != nil || storage != 192 {
		t.Fatal("container storage not reserved", storage, err)
	}
}
