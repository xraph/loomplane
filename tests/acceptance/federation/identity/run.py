#!/usr/bin/env python3
"""Build and run the local SPIRE qualification fixture, then remove owned artifacts."""

import argparse
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import uuid


ROOT = Path(__file__).resolve().parents[4]
FIXTURE = Path(__file__).resolve().parent


def run(with_postgres: bool = False) -> None:
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
    network = f"loomplane-identity-network-{identity}"
    database = f"loomplane-identity-postgres-{identity}"
    postgres_image = "postgres:17.11-alpine@sha256:b0f9560a2de083e2cc7382e75f808c7381a32852a7ec49117deedb300e552b24"
    runtime_options = ["--network", "none"]
    try:
        if with_postgres:
            password = uuid.uuid4().hex
            subprocess.run(["docker", "network", "create", "--internal", network], check=True, stdout=subprocess.DEVNULL)
            subprocess.run([
                "docker", "run", "-d", "--rm", "--name", database,
                "--network", network, "--network-alias", "postgres",
                "--tmpfs", "/var/lib/postgresql/data:rw,size=512m",
                "--env", "POSTGRES_USER=fixture", "--env", f"POSTGRES_PASSWORD={password}",
                "--env", "POSTGRES_DB=fixture", postgres_image,
            ], check=True, stdout=subprocess.DEVNULL)
            runtime_options = ["--network", network, "--env", f"LOOMPLANE_FIXTURE_DATABASE_URL=postgres://fixture:{password}@postgres:5432/fixture?sslmode=disable"]
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
                ["docker", "run", "--rm", *runtime_options, "--platform", f"linux/{architecture}", "--name", name, tag],
                check=True,
            )
    finally:
        # These names contain this run's random ID. Shared images and containers
        # are never selected for cleanup.
        subprocess.run(["docker", "rm", "-f", name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)
        if with_postgres:
            subprocess.run(["docker", "rm", "-f", database], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)
            subprocess.run(["docker", "network", "rm", network], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)
        subprocess.run(["docker", "image", "rm", tag], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--postgres", action="store_true", help="Join live SPIRE transport to a disposable PostgreSQL enrollment store")
    run(parser.parse_args().postgres)
