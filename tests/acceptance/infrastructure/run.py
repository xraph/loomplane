#!/usr/bin/env python3
"""Run an application acceptance suite and always clean its recorded targets."""

import argparse
import os
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
SETUP = Path(__file__).with_name("setup.py")
# Each suite is its build tags, the test it runs and how long it may take.
SUITES = {
    "infrastructure": ("integration", "^TestAcceptanceInfrastructure$", "90m"),
    "catalog": ("integration acceptance", "^TestAcceptanceCatalogComposed$", "60m"),
}

parser = argparse.ArgumentParser()
parser.add_argument("--suite", choices=sorted(SUITES), default="infrastructure")
tags, test, timeout = SUITES[parser.parse_args().suite]
state = os.environ.get("LOOMPLANE_INFRASTRUCTURE_STATE")
if not state:
    state = subprocess.check_output(
        [sys.executable, str(SETUP)], cwd=ROOT, text=True
    ).strip()
print("Infrastructure ownership record: " + state, flush=True)
try:
    result = subprocess.run(
        [
            "go",
            "test",
            "-tags=" + tags,
            "./tests/integration",
            "-run",
            test,
            "-count=1",
            "-timeout=" + timeout,
            "-v",
        ],
        cwd=ROOT,
        env={**os.environ, "LOOMPLANE_INFRASTRUCTURE_STATE": state},
        check=False,
    )
finally:
    subprocess.run(
        [sys.executable, str(SETUP), "--cleanup", state], cwd=ROOT, check=True
    )
sys.exit(result.returncode)
