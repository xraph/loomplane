package recipes

import (
	"reflect"
	"testing"
)

func TestKubernetesPostgresPinsStorageAlongsideImage(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		recipe, err := KubernetesPostgres("17", arch)
		if err != nil {
			t.Fatal(err)
		}
		docker, err := Postgres("17", arch)
		if err != nil {
			t.Fatal(err)
		}
		if recipe.ID == docker.ID || recipe.Image != docker.Image || recipe.VolumeSizeMB != 10240 || docker.VolumeSizeMB != 0 || recipe.Revision != "1" {
			t.Fatalf("cluster storage is not part of the immutable recipe: %+v %+v", recipe, docker)
		}
	}
	for _, pair := range [][2]string{{"18", "arm64"}, {"17", "unknown"}} {
		if _, err := KubernetesPostgres(pair[0], pair[1]); err == nil {
			t.Fatalf("unqualified recipe: %v", pair)
		}
	}
}

func TestKubernetesCacheRecipe(t *testing.T) {
	for _, test := range []struct{ kind, version string }{{"redis", "8.2"}, {"valkey", "8.1"}} {
		for _, arch := range []string{"amd64", "arm64"} {
			recipe, err := KubernetesCache(test.kind, test.version, arch)
			if err != nil {
				t.Fatal(err)
			}
			docker, err := Lookup(test.kind, test.version, arch)
			if err != nil {
				t.Fatal(err)
			}
			if recipe.ID != test.kind+"-"+test.version+"-kubernetes" || recipe.Image != docker.Image || recipe.VolumeSizeMB != 2048 || recipe.Engine() != test.kind || docker.VolumeSizeMB != 0 {
				t.Fatalf("cache cluster recipe: %+v %+v", recipe, docker)
			}
			looked, err := LookupForTarget(test.kind, test.version, arch, "kubernetes")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(looked, recipe) {
				t.Fatalf("LookupForTarget: %+v, want %+v", looked, recipe)
			}
		}
	}
	for _, args := range [][3]string{{"redis", "7.4", "amd64"}, {"valkey", "8.1", "riscv64"}, {"postgres", "17", "amd64"}, {"mysql", "8.4", "amd64"}} {
		if _, err := KubernetesCache(args[0], args[1], args[2]); err == nil {
			t.Fatalf("unqualified cache recipe: %v", args)
		}
	}
	if !KubernetesRecovery("postgres") || KubernetesRecovery("redis") || KubernetesRecovery("valkey") || KubernetesRecovery("timescaledb") {
		t.Fatal("only postgres recovers on Kubernetes")
	}
}
