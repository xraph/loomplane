package agentinstall

import (
	"encoding/json"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
)

func TestCSIInstallationContract(t *testing.T) {
	cfg := DefaultCSIConfig("spire")
	list, err := CSIManifest(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 3 {
		t.Fatal("expected account, driver and daemon")
	}
	raw, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "Secret") || strings.Contains(string(raw), "ClusterRole") {
		t.Fatal("unexpected API authority")
	}
	account := list.Items[0].Object.(*corev1.ServiceAccount)
	driver := list.Items[1].Object.(*storagev1.CSIDriver)
	daemon := list.Items[2].Object.(*appsv1.DaemonSet)
	if account.AutomountServiceAccountToken == nil || *account.AutomountServiceAccountToken || driver.Spec.AttachRequired == nil || *driver.Spec.AttachRequired || !*driver.Spec.PodInfoOnMount || len(driver.Spec.VolumeLifecycleModes) != 1 || driver.Spec.VolumeLifecycleModes[0] != storagev1.VolumeLifecycleEphemeral {
		t.Fatal("unsafe CSI registration")
	}
	pod := daemon.Spec.Template.Spec
	if pod.ServiceAccountName != account.Name || pod.NodeSelector["kubernetes.io/os"] != "linux" || pod.HostPID || pod.HostIPC || pod.HostNetwork || *pod.AutomountServiceAccountToken || len(pod.Containers) != 2 {
		t.Fatal("unsafe driver pod")
	}
	if !*pod.Containers[0].SecurityContext.Privileged || !*pod.Containers[0].SecurityContext.ReadOnlyRootFilesystem {
		t.Fatal("driver security differs")
	}
	for _, v := range pod.Volumes {
		if v.HostPath == nil || v.HostPath.Path == "/" {
			t.Fatal("unbounded node mount")
		}
	}
	cfg.KubeletRootDirectory = "/custom/kubelet"
	cfg.AgentSocketDirectory = "/custom/spire/sockets"
	custom, err := CSIManifest(cfg)
	if err != nil {
		t.Fatal(err)
	}
	bytes, err := json.Marshal(custom)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(bytes), "/var/lib/kubelet") || strings.Contains(string(bytes), "/run/spire/agent-sockets") {
		t.Fatal("custom paths not propagated")
	}
	for _, mutate := range []func(*CSIConfig){
		func(c *CSIConfig) { c.Namespace = "../spire" },
		func(c *CSIConfig) { c.DriverImage = "driver:latest" },
		func(c *CSIConfig) { c.RegistrarImage = "registrar:latest" },
		func(c *CSIConfig) { c.KubeletRootDirectory = "/" },
		func(c *CSIConfig) { c.AgentSocketDirectory = "/run/../etc" },
		func(c *CSIConfig) { c.AgentSocketDirectory = c.KubeletRootDirectory },
		func(c *CSIConfig) { c.KubeletRootDirectory = "relative" },
	} {
		bad := DefaultCSIConfig("spire")
		mutate(&bad)
		if _, err = CSIManifest(bad); err == nil {
			t.Fatal("invalid installation accepted", bad)
		}
	}
}
