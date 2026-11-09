"""Guard against unbounded runs, scope mixing and destructive cleanup plans."""

import copy
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

import run

HERE = Path(__file__).parent


def config():
    return {
        "version": 1,
        "run_id": "fleet-check-001",
        "target": {
            "api_url": "http://127.0.0.1:8090",
            "org_id": "org-fixture",
            "fleet_id": "fleet-fixture",
            "stage_id": "stage-fixture",
            "cloud_id": "cloud-fixture",
            "region": "region-fixture",
            "runtime": "kubernetes",
        },
        "budget": {"currency": "USD", "max_minor_units": 100},
        "bounds": {
            "max_instances": 2,
            "max_replicas_per_service": 1,
            "max_cpu_millis": 2000,
            "max_memory_mib": 640,
            "max_storage_gib": 2,
            "max_buckets": 2,
            "max_duration_seconds": 600,
        },
        "declared_capabilities": [
            "namespace_isolation",
            "pod_groups",
            "lifecycle_jobs",
            "database_credentials",
            "bucket_credentials",
        ],
        "cleanup": {"policy": "retain_data", "resource_ids": []},
    }


class PreflightTests(unittest.TestCase):
    def test_valid_plan_accounts_for_sidecars_jobs_and_control(self):
        report = run.plan(config())
        self.assertEqual(report["status"], "dry_run_validated")
        self.assertFalse(report["live_qualified"])
        self.assertEqual(report["demand"]["cpu_millis"], 2000)
        self.assertEqual(report["demand"]["memory_mib"], 640)
        self.assertEqual(len(report["instances"]), 2)
        self.assertTrue(all(c["status"] == "not_run" for c in report["live_checks"]))

    def test_missing_target_budget_and_each_bound_are_rejected(self):
        for field in ("target", "budget", "bounds"):
            value = config()
            del value[field]
            with self.subTest(field=field), self.assertRaises(ValueError):
                run.plan(value)
        for field in config()["bounds"]:
            value = config()
            del value["bounds"][field]
            with self.subTest(bound=field), self.assertRaises(ValueError):
                run.plan(value)

    def test_invalid_numbers_and_unknown_keys_cannot_disable_bounds(self):
        for bad in (0, -1, True, "100", 1.5, None):
            value = config()
            value["budget"]["max_minor_units"] = bad
            with self.subTest(bad=bad), self.assertRaises(ValueError):
                run.plan(value)
        value = config()
        value["bounds"]["max_memroy_mib"] = 100000
        with self.assertRaises(ValueError):
            run.plan(value)

    def test_sidecar_and_job_demand_cannot_exceed_bounds(self):
        for field, limit in (
            ("max_cpu_millis", 1999),
            ("max_memory_mib", 639),
            ("max_instances", 1),
            ("max_buckets", 1),
            ("max_storage_gib", 1),
        ):
            value = config()
            value["bounds"][field] = limit
            with self.subTest(field=field), self.assertRaises(ValueError):
                run.plan(value)

    def test_missing_capability_and_unsupported_runtime_fail_closed(self):
        value = config()
        value["declared_capabilities"].remove("namespace_isolation")
        with self.assertRaises(ValueError):
            run.plan(value)
        value = config()
        value["target"]["runtime"] = "unsupported"
        with self.assertRaises(ValueError):
            run.plan(value)

    def test_mixed_org_and_duplicate_instances_are_rejected(self):
        fixture = run.workload(config())
        fixture["instances"][1]["org_id"] = "other-org"
        with self.assertRaises(ValueError):
            run.plan(config(), fixture)
        fixture = run.workload(config())
        fixture["instances"][1] = copy.deepcopy(fixture["instances"][0])
        with self.assertRaises(ValueError):
            run.plan(config(), fixture)

    def test_unsafe_cleanup_and_secret_in_url_are_rejected(self):
        for policy in ("delete_all", "delete_attached_data", ""):
            value = config()
            value["cleanup"]["policy"] = policy
            with self.subTest(policy=policy), self.assertRaises(ValueError):
                run.plan(value)
        for url in (
            "https://token@example.invalid",
            "http://example.invalid",
            "https://example.invalid/?token=secret",
            "file:///tmp/x",
        ):
            value = config()
            value["target"]["api_url"] = url
            with self.subTest(url=url), self.assertRaises(ValueError):
                run.plan(value)

    def test_cleanup_report_keeps_data_and_refuses_identity_drift(self):
        value = config()
        receipt = {
            "version": 1,
            "run_id": value["run_id"],
            "target": value["target"],
            "resources": [
                {
                    "id": "database-001",
                    "kind": "database",
                    "org_id": "org-fixture",
                    "run_id": value["run_id"],
                    "created_by_run": True,
                },
                {
                    "id": "workload-001",
                    "kind": "workload",
                    "org_id": "org-fixture",
                    "run_id": value["run_id"],
                    "created_by_run": True,
                },
            ],
        }
        value["cleanup"]["resource_ids"] = ["database-001", "workload-001"]
        result = run.cleanup_plan(value, receipt)
        self.assertEqual(result["retained_data_ids"], ["database-001"])
        self.assertEqual(
            result["residual_resource_ids"], ["database-001", "workload-001"]
        )
        self.assertEqual(result["deleted_resource_ids"], [])
        receipt["resources"][1]["run_id"] = "another-run"
        with self.assertRaises(ValueError):
            run.cleanup_plan(value, receipt)

    def test_cleanup_refuses_unrecorded_resources_and_changed_target(self):
        value = config()
        receipt = {
            "version": 1,
            "run_id": value["run_id"],
            "target": copy.deepcopy(value["target"]),
            "resources": [],
        }
        value["cleanup"]["resource_ids"] = ["unrecorded"]
        with self.assertRaises(ValueError):
            run.cleanup_plan(value, receipt)
        value["cleanup"]["resource_ids"] = []
        receipt["target"]["cloud_id"] = "other-cloud"
        with self.assertRaises(ValueError):
            run.cleanup_plan(value, receipt)

    def test_cli_dry_run_and_live_blocker_do_not_create_resources(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "config.json"
            path.write_text(json.dumps(config()))
            command = [sys.executable, str(HERE / "run.py"), "--config", str(path)]
            result = subprocess.run(
                command + ["--dry-run"],
                capture_output=True,
                text=True,
                check=False,
                timeout=5,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(json.loads(result.stdout)["status"], "dry_run_validated")
            result = subprocess.run(
                command + ["--live"],
                capture_output=True,
                text=True,
                check=False,
                timeout=5,
            )
            self.assertEqual(result.returncode, 2)
            self.assertIn("live_adapter_unavailable", result.stdout)
            self.assertEqual(list(Path(directory).iterdir()), [path])


if __name__ == "__main__":
    unittest.main()
