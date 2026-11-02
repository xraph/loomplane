"""Cohort discovery and distribution must preserve every test exactly once."""

import importlib.util
import subprocess
import unittest
from pathlib import Path
from unittest import mock

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

    def test_minimal_and_unicode_identifiers_survive_discovery(self):
        self.assertEqual(cohorts.discover("Test\n"), ["Test"])
        names = ["Test", "TestÉcriture", "Test日本語", "Test_A", "Test9"]
        found = cohorts.discover("\n".join(names) + "\nok package 1.0s\n")
        executed = [
            name
            for shard in range(8)
            for _, group in cohorts.select(cohorts.partition(found), shard, 8)
            for name in group
        ]
        self.assertEqual(sorted(executed), sorted(names))

    def test_compile_once_and_execute_every_cohort_from_same_binary(self):
        names = [f"TestCase{i:03}" for i in range(25)]
        for shard in range(2):
            with (
                mock.patch.object(cohorts.subprocess, "run") as run,
                mock.patch.object(
                    cohorts.subprocess, "check_output", return_value="\n".join(names)
                ) as listing,
            ):
                cohorts.run_cohorts(shard, 2)
            build = run.call_args_list[0]
            binary = build.args[0][-2]
            self.assertEqual(
                build.args[0],
                [
                    "go",
                    "test",
                    "-c",
                    "-race",
                    "-tags=integration",
                    "-o",
                    binary,
                    cohorts.PACKAGE,
                ],
            )
            self.assertEqual(build.kwargs["timeout"], 600)
            self.assertTrue(build.kwargs["check"])
            package_dir = build.kwargs["cwd"] / "tests/integration"
            listing.assert_called_once_with(
                [binary, "-test.list=^Test"], cwd=package_dir, text=True, timeout=30
            )
            selected = cohorts.select(cohorts.partition(names), shard, 2)
            self.assertEqual(len(run.call_args_list), 1 + len(selected))
            for call, (_, group) in zip(run.call_args_list[1:], selected):
                self.assertEqual(
                    call.args[0],
                    [
                        binary,
                        "-test.count=1",
                        "-test.timeout=10m",
                        "-test.run=^(" + "|".join(group) + ")$",
                    ],
                )
                self.assertEqual(
                    call.kwargs, {"cwd": package_dir, "check": True, "timeout": 660}
                )
            self.assertFalse(Path(binary).parent.exists())

    def test_failure_and_timeout_remove_only_owned_binary_directory(self):
        for phase in ("compile", "list", "runtime"):
            for failure in (
                subprocess.CalledProcessError(1, phase),
                subprocess.TimeoutExpired(phase, 1),
            ):
                paths = []

                def run(command, phase=phase, failure=failure, paths=paths, **kwargs):
                    if command[0] == "go":
                        binary = Path(command[-2])
                        paths.append(binary)
                        binary.write_text("owned binary")
                        if phase == "compile":
                            raise failure
                    elif phase == "runtime":
                        raise failure

                def listing(*args, phase=phase, failure=failure, **kwargs):
                    if phase == "list":
                        raise failure
                    return "TestA\n"

                with (
                    mock.patch.object(cohorts.subprocess, "run", side_effect=run),
                    mock.patch.object(
                        cohorts.subprocess, "check_output", side_effect=listing
                    ),
                    self.assertRaises(type(failure)),
                ):
                    cohorts.run_cohorts(0, 1)
                self.assertEqual(len(paths), 1)
                self.assertFalse(paths[0].parent.exists())

    def test_invalid_shard_does_not_build(self):
        with mock.patch.object(cohorts.subprocess, "run") as run:
            with self.assertRaises(ValueError):
                cohorts.run_cohorts(8, 8)
            run.assert_not_called()

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
