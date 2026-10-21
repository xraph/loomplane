import unittest

from run import images, manifests, assert_qualified


class FixtureTests(unittest.TestCase):
    def test_skipped_or_missing_live_probe_cannot_pass(self):
        for log in (
            "PASS",
            "--- SKIP: TestPreparedKubernetesIdentity (0.00s)\nPASS",
            "--- FAIL: TestPreparedKubernetesIdentity (1.00s)",
        ):
            with self.subTest(log=log), self.assertRaises(RuntimeError):
                assert_qualified(log, 0)
        assert_qualified("--- PASS: TestPreparedKubernetesIdentity (73.00s)\nPASS", 0)
        with self.assertRaises(RuntimeError):
            assert_qualified("--- PASS: TestPreparedKubernetesIdentity (73.00s)", 1)

    def test_every_dependency_is_digest_pinned(self):
        for image in images.values():
            self.assertRegex(image, r"@sha256:[a-f0-9]{64}$")

    def test_admin_stays_local_and_workload_uses_csi(self):
        docs = manifests("fixture@sha256:" + "a" * 64, "certificate")
        pod = next(d for d in docs if d["kind"] == "Pod")
        volumes = {v["name"]: v for v in pod["spec"]["volumes"]}
        self.assertEqual(volumes["workload"]["csi"]["driver"], "csi.spiffe.io")
        self.assertIn("emptyDir", volumes["admin"])
        self.assertFalse(any("hostPath" in v for v in volumes.values()))
        service_ports = [
            p["port"]
            for d in docs
            if d["kind"] == "Service"
            for p in d["spec"]["ports"]
        ]
        self.assertEqual(service_ports, [8081, 9443])

    def test_application_controller_permissions_stay_in_apps(self):
        docs = manifests("fixture@sha256:" + "a" * 64, "certificate")
        roles = [d for d in docs if d["kind"] == "Role" and d["metadata"]["name"] == "fixture"]
        apps = next(d for d in roles if d["metadata"]["namespace"] == "apps")
        controllers = [r for r in apps["rules"] if r["apiGroups"] == ["apps"]]
        self.assertEqual(controllers, [
            {"apiGroups": ["apps"], "resources": ["deployments"], "verbs": ["get", "create", "delete"]},
            {"apiGroups": ["apps"], "resources": ["replicasets"], "verbs": ["get"]},
        ])
        spire = next(d for d in roles if d["metadata"]["namespace"] == "spire")
        self.assertFalse(any(r["apiGroups"] == ["apps"] for r in spire["rules"]))

    def test_kubelet_tls_is_verified(self):
        docs = manifests("fixture@sha256:" + "a" * 64, "pinned-kubelet-ca")
        cm = next(
            d
            for d in docs
            if d["kind"] == "ConfigMap" and d["metadata"]["name"] == "spire-agent"
        )
        self.assertNotIn("skip_kubelet_verification", cm["data"]["agent.conf"])
        self.assertIn(
            'kubelet_ca_path = "/config/kubelet.crt"', cm["data"]["agent.conf"]
        )
        self.assertEqual(cm["data"]["kubelet.crt"], "pinned-kubelet-ca")


if __name__ == "__main__":
    unittest.main()
