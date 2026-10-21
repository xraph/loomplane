package agentinstall

import (
	"testing"
)

func TestLaunchDigestPinsExactConfigurationAndPod(t *testing.T) {
	plan, _, record, settings := launchFixture(t, false)
	desired, err := plan.DesiredConfigMap(record, settings)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := ConfigMapDigest(desired)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"data", "owner", "namespace", "mutable", "extra data"} {
		t.Run(change, func(t *testing.T) {
			copyMap := desired.DeepCopy()
			switch change {
			case "data":
				copyMap.Data[ConfigKey] = "{}"
			case "owner":
				copyMap.OwnerReferences[0].UID = "another-pod"
			case "namespace":
				copyMap.Namespace = "elsewhere"
			case "mutable":
				copyMap.Immutable = nil
			case "extra data":
				copyMap.Data["unrelated"] = "value"
			}
			actual, hashErr := ConfigMapDigest(copyMap)
			if hashErr == nil && actual == digest {
				t.Fatal("different configuration matched saved intent")
			}
		})
	}
	desired.ResourceVersion = "123"
	desired.UID = "created-uid"
	actual, err := ConfigMapDigest(desired)
	if err != nil || actual != digest {
		t.Fatal("server metadata changed content identity", err)
	}
}
