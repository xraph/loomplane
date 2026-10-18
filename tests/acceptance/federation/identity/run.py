#!/usr/bin/env python3
"""Build and run the local SPIRE qualification fixture, then remove owned artifacts."""

import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import uuid


ROOT = Path(__file__).resolve().parents[4]
FIXTURE = Path(__file__).resolve().parent


def run() -> None:
    architecture = subprocess.check_output(
        ["docker", "info", "--format", "{{.Architecture}}"], text=True
    ).strip()
    architecture = {"aarch64": "arm64", "x86_64": "amd64"}.get(
        architecture, architecture
    )
    if architecture not in {"arm64", "amd64"}:
        raise SystemExit(f"Unsupported fixture architecture: {architecture}")
    identity = uuid.uuid4().hex[:12]
    tag = f"loomplane-identity-fixture:{identity}"
    name = f"loomplane-identity-fixture-{identity}"
    try:
        with tempfile.TemporaryDirectory(prefix="loomplane-identity-") as folder:
            context = Path(folder)
            subprocess.run(
                [
                    "go", "build", "-o", str(context / "identity-fixture"),
                    "./tests/acceptance/federation/identity",
                ],
                cwd=ROOT,
                env={**os.environ, "GOOS": "linux", "GOARCH": architecture, "CGO_ENABLED": "0"},
                check=True,
            )
            shutil.copyfile(FIXTURE / "Dockerfile", context / "Dockerfile")
            subprocess.run(
                ["docker", "build", "--quiet", "--platform", f"linux/{architecture}", "-t", tag, str(context)],
                check=True,
            )
            subprocess.run(
                ["docker", "run", "--rm", "--network", "none", "--platform", f"linux/{architecture}", "--name", name, tag],
                check=True,
            )
    finally:
        # These names contain this run's random ID. Shared images and containers
        # are never selected for cleanup.
        subprocess.run(["docker", "rm", "-f", name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)
        subprocess.run(["docker", "image", "rm", tag], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)


if __name__ == "__main__":
    run()
