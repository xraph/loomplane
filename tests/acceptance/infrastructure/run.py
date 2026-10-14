#!/usr/bin/env python3
"""Run the application acceptance suite and always clean its recorded targets."""

import os
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
SETUP = Path(__file__).with_name("setup.py")
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
            "-tags=integration",
            "./tests/integration",
            "-run",
            "^TestAcceptanceInfrastructure$",
            "-count=1",
            "-timeout=90m",
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
