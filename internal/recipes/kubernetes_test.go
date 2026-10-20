package recipes

import "testing"

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
