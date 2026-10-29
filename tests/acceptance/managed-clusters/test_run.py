import importlib.util
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("managed_clusters_run", Path(__file__).with_name("run.py"))
runner = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runner)


def config():
    return {"api_url": "http://127.0.0.1:8792", "cloud_id": "cloud", "account_id": "account",
            "project_id": "project", "region": "nyc1", "disposable": True, "qualification_only": True,
            "max_nodes": 1, "timeout_seconds": 30, "max_node_hourly_micro_usd": 100000,
            "input": {"kind": "create", "provider": "digitalocean", "account_id": "account",
                      "worker_image": "worker@sha256:" + "a" * 64,
                      "attachment": {"project_id": "project", "prerequisites": {}},
                      "spec": {"region": "nyc1", "node_pools": [{"count": 1}]}},
            "commands": {step: ["fixture-only"] for step in runner.STEPS}}


class QualificationTests(unittest.TestCase):
    def test_requires_bounded_disposable_scope(self):
        for key, value in (("disposable", False), ("max_nodes", 0), ("timeout_seconds", 3601)):
            sample = config()
            sample[key] = value
            with self.assertRaises(ValueError):
                runner.validate(sample)

    def test_resume_never_previews_twice_and_retains_cleanup_gate(self):
        calls = []
        def request(method, path, _body=None):
            calls.append((method, path))
            if path.endswith("preview"):
                return {"id": "op", "digest": "exact", "estimated_node_hourly_micro_usd": 100}
            return {"status": "succeeded", "resource_id": "resource", "enrollment": {"runtime_verified": True}}
        def command(_step, state):
            return {**state, "passed": True, "remaining_resources": []}
        with tempfile.TemporaryDirectory() as directory:
            state = Path(directory) / "state.json"
            first = runner.Runner(config(), state, "unused", request, command).run()
            runner.Runner(config(), state, "unused", request, command).run()
        self.assertEqual(sum(path.endswith("preview") for _, path in calls), 1)
        self.assertEqual(first["completed"], list(runner.STEPS))
        self.assertFalse(first["general_purchase_rollout"])

    def test_rejects_cleanup_with_remaining_resources(self):
        with tempfile.TemporaryDirectory() as directory:
            item = runner.Runner(config(), Path(directory) / "state.json", "unused",
                                 command=lambda _step, state: {**state, "passed": True, "remaining_resources": ["cluster"]})
            item.state.update(operation_id="op", resource_id="resource")
            with self.assertRaises(ValueError):
                item.run_step("cleanup_inventory")


if __name__ == "__main__":
    unittest.main()
