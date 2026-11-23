package connections

import "testing"

func TestArtifactRepositoryNames(t *testing.T) {
	for name, ok := range map[string]bool{
		"acme-prod/images": true, "my-project-1/loomplane": true,
		"acme": false, "acme-prod/": false, "Acme-prod/images": false, "acme-prod/images/extra": false, "../images": false,
	} {
		if artifactRepository.MatchString(name) != ok {
			t.Errorf("%q: ok=%v", name, ok)
		}
	}
}
