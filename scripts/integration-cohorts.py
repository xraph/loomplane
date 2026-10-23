#!/usr/bin/env python3
"""Run every discovered integration test in bounded, disjoint processes."""

import argparse
import re
import subprocess
import tempfile
from pathlib import Path

PACKAGE = "./tests/integration/"


def discover(output):
    names = [line for line in output.splitlines() if re.fullmatch(r"Test\w*", line)]
    if not names or len(set(names)) != len(names):
        raise ValueError("test discovery is empty or contains duplicates")
    return sorted(names)


def partition(names, size=12):
    if size <= 0 or len(names) != len(set(names)) or not names:
        raise ValueError("invalid cohort input")
    ordered = sorted(names)
    return [ordered[start : start + size] for start in range(0, len(ordered), size)]


def select(cohorts, shard, count):
    if count < 1 or not 0 <= shard < count:
        raise ValueError("invalid shard")
    return [
        (index, group) for index, group in enumerate(cohorts) if index % count == shard
    ]


def run_cohorts(shard, count):
    select([], shard, count)  # Reject invalid arguments before compiling.
    root = Path(__file__).resolve().parents[1]
    with tempfile.TemporaryDirectory(prefix="loomplane-integration-") as directory:
        binary = str(Path(directory) / "integration.test")
        print("Compiling race-enabled integration binary (600s limit)", flush=True)
        subprocess.run(
            ["go", "test", "-c", "-race", "-tags=integration", "-o", binary, PACKAGE],
            cwd=root,
            check=True,
            timeout=600,
        )
        output = subprocess.check_output(
            [binary, "-test.list=^Test"],
            cwd=root / "tests/integration",
            text=True,
            timeout=30,
        )
        names = discover(output)
        groups = partition(names)
        print(f"Discovered {len(names)} tests in {len(groups)} cohorts", flush=True)
        for index, group in select(groups, shard, count):
            expression = "^(" + "|".join(re.escape(name) for name in group) + ")$"
            print(f"Cohort {index + 1}/{len(groups)}: {', '.join(group)}", flush=True)
            subprocess.run(
                [
                    binary,
                    "-test.count=1",
                    "-test.timeout=10m",
                    "-test.run=" + expression,
                ],
                cwd=root / "tests/integration",
                check=True,
                timeout=660,
            )


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--shard", type=int, default=0)
    parser.add_argument("--shards", type=int, default=1)
    args = parser.parse_args()
    run_cohorts(args.shard, args.shards)


if __name__ == "__main__":
    main()
