#!/usr/bin/env python3
"""Run every discovered integration test in bounded, disjoint processes."""

import argparse
import re
import subprocess

PACKAGE = "./tests/integration/"


def discover(output):
    names = [line for line in output.splitlines() if re.fullmatch(r"Test\w+", line)]
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


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--shard", type=int, default=0)
    parser.add_argument("--shards", type=int, default=1)
    args = parser.parse_args()
    output = subprocess.check_output(
        ["go", "test", "-tags=integration", "-list", "^Test", PACKAGE],
        text=True,
        timeout=300,
    )
    names = discover(output)
    cohorts = partition(names)
    print(f"Discovered {len(names)} tests in {len(cohorts)} cohorts", flush=True)
    for index, group in select(cohorts, args.shard, args.shards):
        expression = "^(" + "|".join(re.escape(name) for name in group) + ")$"
        print(f"Cohort {index + 1}/{len(cohorts)}: {', '.join(group)}", flush=True)
        subprocess.run(
            [
                "go",
                "test",
                "-race",
                "-count=1",
                "-tags=integration",
                "-timeout=10m",
                "-run",
                expression,
                PACKAGE,
            ],
            check=True,
            timeout=660,
        )


if __name__ == "__main__":
    main()
