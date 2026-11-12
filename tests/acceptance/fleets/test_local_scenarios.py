"""Run the process scenarios against the existing synthetic workload."""

import os
import tempfile
import unittest
from pathlib import Path

import local_scenarios
from fixture import Store


class LocalScenariosTests(unittest.TestCase):
    def test_separate_processes_reject_neighbor_reads_and_writes(self):
        local_scenarios.check_isolation()

    def test_killed_worker_rolls_back_partial_results_and_replays_once(self):
        local_scenarios.check_crash_recovery()

    def test_crash_checkpoint_observes_an_actual_uncommitted_result(self):
        with local_scenarios.state_directory() as directory:
            store = Store(directory, "instance-a")
            store.bootstrap()
            store.enqueue("job-a", "synthetic")
            store.enqueue("job-b", "synthetic")
            with local_scenarios.child(
                local_scenarios.interrupted_worker, directory
            ) as (_, channel):
                self.assertEqual(
                    local_scenarios.receive(channel),
                    {"uncommitted_results": 1, "transaction_open": True},
                )
                self.assertEqual(store.metrics()["completed_jobs"], 0)

    def test_snapshot_restores_both_adapters_without_changing_source(self):
        local_scenarios.check_snapshot_restore()

    def test_failed_scenario_is_reported_without_exception_details(self):
        def broken():
            raise RuntimeError("private synthetic sentinel")

        report = local_scenarios.run_checks({"broken": broken})
        self.assertEqual(report["status"], "failed")
        self.assertEqual(report["checks"], [{"name": "broken", "status": "failed"}])
        self.assertNotIn("sentinel", str(report))
        self.assertFalse(report["live_qualified"])
        self.assertTrue(all(check["status"] == "not_run" for check in report["live_checks"]))

    def test_runner_cleans_its_temporary_state_after_failure(self):
        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaises(RuntimeError):
                with local_scenarios.state_directory(directory) as state:
                    (state / "data").write_text("synthetic")
                    raise RuntimeError("failure")
            self.assertEqual(list(Path(directory).iterdir()), [])

    def test_failure_reaps_only_the_owned_child_process(self):
        with local_scenarios.state_directory() as directory:
            with self.assertRaises(RuntimeError):
                with local_scenarios.child(
                    local_scenarios.serve,
                    directory,
                    "instance-a",
                    "api",
                    "synthetic-token",
                ) as (process, channel):
                    local_scenarios.receive(channel)
                    pid = process.pid
                    raise RuntimeError("scenario failed")
            with self.assertRaises(ProcessLookupError):
                os.kill(pid, 0)


if __name__ == "__main__":
    unittest.main()
