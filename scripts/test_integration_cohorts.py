"""Cohort discovery and distribution must preserve every test exactly once."""

import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location(
    "cohorts", Path(__file__).with_name("integration-cohorts.py")
)
cohorts = importlib.util.module_from_spec(spec)
spec.loader.exec_module(cohorts)


class CohortTests(unittest.TestCase):
    def test_disjoint_exhaustive_across_every_matrix_shard(self):
        for total in (1, 11, 12, 13, 108, 487):
            names = [f"TestCase{i:04}" for i in range(total)]
            groups = cohorts.partition(list(reversed(names)))
            executed = [
                name
                for shard in range(8)
                for _, group in cohorts.select(groups, shard, 8)
                for name in group
            ]
            self.assertEqual(sorted(executed), names)
            self.assertEqual(len(executed), len(set(executed)))
            self.assertTrue(all(1 <= len(group) <= 12 for group in groups))

    def test_discovery_excludes_tool_output_and_keeps_new_names(self):
        self.assertEqual(
            cohorts.discover("TestZ\nTestNewTest\nok example 0.1s\nTestA\n"),
            ["TestA", "TestNewTest", "TestZ"],
        )

    def test_invalid_discovery_and_partition_fail_closed(self):
        for output in ("ok package", "TestA\nTestA"):
            with self.assertRaises(ValueError):
                cohorts.discover(output)
        with self.assertRaises(ValueError):
            cohorts.partition(["TestA", "TestA"])
        for shard, count in ((-1, 8), (8, 8), (0, 0)):
            with self.assertRaises(ValueError):
                cohorts.select([["TestA"]], shard, count)


if __name__ == "__main__":
    unittest.main()
