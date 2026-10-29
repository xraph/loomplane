import copy
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
                      "spec": {"region": "nyc1", "node_pools": [{"name": "workers", "size": "s-2", "count": 1}]}},
            "commands": {step: ["fixture-only"] for step in runner.STEPS}}


def review():
    return {"id": "op", "cloud_id": "cloud", "digest": "exact", "status": "preview",
            "input": copy.deepcopy(config()["input"]), "estimated_node_hourly_micro_usd": 100,
            "unknown_costs": ["load_balancers"], "resource_id": "resource",
            "enrollment": {"runtime_verified": True}}


class QualificationTests(unittest.TestCase):
    def exercise(self, operation, saved=False, sample=None):
        calls = []
        current = copy.deepcopy(operation)
        def request(method, path, _body=None):
            calls.append((method, path))
            if path.endswith("apply"):
                current["status"] = "succeeded"
            return copy.deepcopy(current)
        def command(_step, state):
            return {**state, "passed": True, "remaining_resources": []}
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "state.json"
            if saved:
                initial = runner.Runner(sample or config(), path, "unused")
                initial.state.update(operation_id="op", digest="exact")
                initial.save()
            try:
                runner.Runner(sample or config(), path, "unused", request, command).run()
            except ValueError:
                self.assertFalse(any(p.endswith("apply") for _, p in calls))
                return calls
        self.fail("Expected rejection before apply")

    def test_requires_bounded_disposable_scope(self):
        for key, value in (("disposable", False), ("max_nodes", 0), ("timeout_seconds", 3601)):
            sample = config()
            sample[key] = value
            with self.assertRaises(ValueError):
                runner.validate(sample)

    def test_unknown_or_invalid_node_price_never_applies(self):
        for saved in (False, True):
            for price in (None, 0, -1, float("nan"), float("inf")):
                with self.subTest(saved=saved, price=price):
                    item = review()
                    item["estimated_node_hourly_micro_usd"] = price
                    self.exercise(item, saved)
            item = review()
            item["unknown_costs"] = ["nodes:workers"]
            self.exercise(item, saved)

    def test_saved_preview_reads_authoritative_scope_before_acceptance(self):
        for field, value in (("region", "ams3"), ("node_pools", [{"name": "workers", "size": "s-2", "count": 4}])):
            item = review()
            item["input"]["spec"][field] = value
            calls = self.exercise(item, True)
            self.assertEqual(calls[0], ("GET", "/cluster-operations/op"))
        item = review()
        item["input"]["worker_image"] = "worker@sha256:" + "b" * 64
        self.exercise(item, True)
        item = review()
        sample = config()
        sample["max_node_hourly_micro_usd"] = 50
        self.exercise(item, True, sample)

    def test_saved_config_cannot_change_origin_or_bounds(self):
        for field, value in (("api_url", "http://127.0.0.1:9999"), ("max_node_hourly_micro_usd", 50), ("region", "ams3")):
            with tempfile.TemporaryDirectory() as directory:
                path = Path(directory) / "state.json"
                first = runner.Runner(config(), path, "unused")
                first.save()
                changed = config()
                changed[field] = value
                with self.assertRaises(ValueError):
                    runner.Runner(changed, path, "unused")

    def test_accepted_recovery_never_previews_or_applies_again(self):
        calls = []
        item = review()
        item["status"] = "accepted"
        def request(method, path, _body=None):
            calls.append((method, path))
            result = copy.deepcopy(item)
            item["status"] = "succeeded"
            return result
        def command(_step, state):
            return {**state, "passed": True, "remaining_resources": []}
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "state.json"
            first = runner.Runner(config(), path, "unused", request, command)
            first.state.update(operation_id="op", digest="exact")
            first.save()
            result = runner.Runner(config(), path, "unused", request, command).run()
            runner.Runner(config(), path, "unused", request, command).run()
        self.assertTrue(all(method == "GET" for method, _ in calls))
        self.assertEqual(result["completed"], list(runner.STEPS))
        self.assertFalse(result["general_purchase_rollout"])

    def test_known_price_acceptance_checks_saved_review_before_apply(self):
        calls = []
        current = review()
        current["input"]["spec"]["node_pools"][0].update(autoscale=False, min=0, max=0, labels={})
        def request(method, path, _body=None):
            calls.append((method, path))
            if path.endswith("apply"):
                current["status"] = "succeeded"
            return copy.deepcopy(current)
        def command(_step, state):
            return {**state, "passed": True, "remaining_resources": []}
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "state.json"
            runner.Runner(config(), path, "unused", request, command).run()
            runner.Runner(config(), path, "unused", request, command).run()
        self.assertEqual(calls[:3], [("POST", "/clusters/preview"), ("GET", "/cluster-operations/op"),
                                    ("POST", "/cluster-operations/op/apply")])
        self.assertEqual(sum(path.endswith("apply") for _, path in calls), 1)

    def test_rejects_cleanup_with_remaining_resources(self):
        with tempfile.TemporaryDirectory() as directory:
            item = runner.Runner(config(), Path(directory) / "state.json", "unused",
                                 command=lambda _step, state: {**state, "passed": True, "remaining_resources": ["cluster"]})
            item.state.update(operation_id="op", resource_id="resource")
            with self.assertRaises(ValueError):
                item.run_step("cleanup_inventory")


if __name__ == "__main__":
    unittest.main()
