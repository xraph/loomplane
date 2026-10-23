#!/usr/bin/env python3
"""Prepare, inspect, run an approved live scope, or clean its exact resources."""

import argparse
import datetime
import fcntl
import hashlib
import json
import os
import re
import signal
import socket
import subprocess
import sys
import threading
import time
import uuid
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
HERE = Path(__file__).resolve().parent
TOOLS = ROOT / ".superpowers/sdd/2026-10-20-shared-cloud-infrastructure/live-tools"
BINARY = TOOLS / "cloudflared-2026.10.3"
TUNNEL_HASH = "f249139e0541fa589c987397d867cc16c83ab1236d31eb6782f2e4552ee9cf80"
HOURS = 6
RETAINED_METADATA = "Up to four per-run DigitalOcean operation/spec tags may remain; exact names are receipted and preserved."


def digest(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def save(path, value):
    temporary = path.with_suffix(".tmp")
    descriptor = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(descriptor, "w") as stream:
        json.dump(value, stream, indent=2)
        stream.flush()
        os.fsync(stream.fileno())
    temporary.replace(path)


def process_identity(pid):
    result = subprocess.run(
        ["ps", "-p", str(pid), "-o", "lstart=", "-o", "command="],
        capture_output=True,
        text=True,
        check=False,
    )
    if result.returncode == 1 and not result.stdout.strip():
        return None
    if result.returncode:
        raise RuntimeError("process inspection failed")
    return result.stdout.strip()


def stop_exact(receipt):
    if not receipt:
        return
    current = process_identity(receipt["pid"])
    if current is None:
        return
    if current != receipt["identity"]:
        raise RuntimeError("process identity drift; refusing to signal reused PID")
    os.killpg(receipt["pid"], signal.SIGTERM)
    for _ in range(50):
        if process_identity(receipt["pid"]) is None:
            return
        time.sleep(0.1)
    if process_identity(receipt["pid"]) != receipt["identity"]:
        raise RuntimeError("process identity changed during shutdown")
    os.killpg(receipt["pid"], signal.SIGKILL)
    for _ in range(50):
        if process_identity(receipt["pid"]) is None:
            return
        time.sleep(0.1)
    raise RuntimeError("owned process did not terminate")


class Live:
    def __init__(self, path):
        self.path = Path(path).resolve()
        self.directory = self.path.parent
        self.scope_path = self.directory / "scope.json"
        self.process_path = self.directory / "processes.json"
        self.binary = self.directory / "live.test"
        self.children = {}

    def invoke(self, mode, extra=None, wait=True):
        environment = os.environ.copy()
        environment.setdefault(
            "LOOMPLANE_TEST_DATABASE_URL",
            "postgres://loomplane:loomplane@localhost:55532/loomplane?sslmode=disable",
        )
        environment.update(
            {
                "LOOMPLANE_SHARED_LIVE_STATE": str(self.path),
                "LOOMPLANE_SHARED_LIVE_MODE": mode,
            }
        )
        environment.update(extra or {})
        child = subprocess.Popen(
            [
                str(self.binary),
                "-test.run=^TestAcceptanceSharedCloudLive$",
                "-test.timeout=" + ("6h" if mode == "run" else "5m"),
            ],
            cwd=ROOT,
            env=environment,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            start_new_session=True,
        )
        if not wait:
            return child
        try:
            code = child.wait(timeout=310)
        except subprocess.TimeoutExpired:
            os.killpg(child.pid, signal.SIGKILL)
            child.wait()
            raise RuntimeError(mode + " exceeded its command deadline") from None
        if code:
            raise RuntimeError(
                mode
                + " failed; retained ledger requires inspection (provider errors are not absence)"
            )
        return child

    def prepare(self):
        if self.path.exists() or self.scope_path.exists():
            raise RuntimeError("prepare requires a new state directory")
        self.directory.mkdir(parents=True, mode=0o700, exist_ok=True)
        self.directory.chmod(0o700)
        if digest(BINARY) != TUNNEL_HASH:
            raise RuntimeError("verified cloudflared binary changed")
        result = subprocess.run(
            [
                "go",
                "test",
                "-c",
                "-tags=integration",
                "-o",
                str(self.binary),
                "./tests/integration/",
            ],
            cwd=ROOT,
            env={**os.environ, "GOMAXPROCS": "2"},
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            timeout=300,
            check=False,
        )
        if result.returncode:
            raise RuntimeError("live entrypoint compilation failed")
        self.invoke(
            "prepare", {"LOOMPLANE_SHARED_LIVE_ID": "lp-live-" + uuid.uuid4().hex[:16]}
        )
        state = json.loads(self.path.read_text())
        scope = {
            "run_id": state["run_id"],
            "account": state["account"],
            "compute_connection": "conn_01m5d3vznqec392f2dvc7p9016",
            "dns_connection": "conn_01m5ead3g2ee6bv3yspqs7gsba",
            "dns_team": "team_hLCTejWYCdvK7glFxF4qhH2z",
            "zone": "shipbraid.dev",
            "host_count": 2,
            "size": "s-1vcpu-2gb",
            "region": "nyc3",
            "image": "ubuntu-24-04-x64",
            "hours": HOURS,
            "hourly_each_usd": state["hourly_usd"],
            "compute_total_usd": round(2 * HOURS * state["hourly_usd"], 6),
            "max_compute_usd": 1,
            "backups": False,
            "extra_volumes": False,
            "hostnames": [
                state["run_id"] + "-app" + str(i) + ".shipbraid.dev" for i in range(3)
            ],
            "callback_intermediary": "Cloudflare Quick Tunnel",
            "callback_payload": "one-time enrollment token and public SSH host key; no SSH private key",
            "callback_scope": "POST only, exact persisted org/operation paths, 16 KiB, 10 seconds per request, isolated loopback app",
            "callback_max_minutes": 45,
            "retained_provider_metadata": RETAINED_METADATA,
            "binary_sha256": digest(self.binary),
            "proxy_sha256": digest(HERE / "enrollment_proxy.py"),
            "supervisor_sha256": digest(__file__),
            "cloudflared_sha256": TUNNEL_HASH,
        }
        save(self.scope_path, scope)
        save(self.directory / "enrollment.json", {"paths": [], "deadline": 0})
        print(json.dumps(scope, indent=2))
        print("Approval scope SHA256: " + digest(self.scope_path))

    def checked_scope(self, execution=False):
        scope = json.loads(self.scope_path.read_text())
        state = json.loads(self.path.read_text())
        if scope["run_id"] != state["run_id"] or scope["account"] != state["account"]:
            raise RuntimeError("prepared ownership identity changed")
        if digest(self.binary) != scope["binary_sha256"]:
            raise RuntimeError("prepared cleanup executable changed")
        if not execution:
            return scope
        expected = {
            "run_id": state["run_id"],
            "account": state["account"],
            "host_count": 2,
            "size": "s-1vcpu-2gb",
            "region": "nyc3",
            "image": "ubuntu-24-04-x64",
            "hours": HOURS,
            "hourly_each_usd": state["hourly_usd"],
            "compute_total_usd": round(2 * HOURS * state["hourly_usd"], 6),
            "max_compute_usd": 1,
            "backups": False,
            "extra_volumes": False,
            "zone": "shipbraid.dev",
            "dns_team": "team_hLCTejWYCdvK7glFxF4qhH2z",
            "compute_connection": "conn_01m5d3vznqec392f2dvc7p9016",
            "dns_connection": "conn_01m5ead3g2ee6bv3yspqs7gsba",
            "hostnames": [
                state["run_id"] + "-app" + str(i) + ".shipbraid.dev" for i in range(3)
            ],
            "callback_max_minutes": 45,
            "retained_provider_metadata": RETAINED_METADATA,
        }
        if any(scope.get(key) != value for key, value in expected.items()):
            raise RuntimeError(
                "prepared approval scope differs from fixed execution scope"
            )
        if (
            digest(self.binary) != scope["binary_sha256"]
            or digest(HERE / "enrollment_proxy.py") != scope["proxy_sha256"]
            or digest(__file__) != scope["supervisor_sha256"]
            or digest(BINARY) != scope["cloudflared_sha256"]
        ):
            raise RuntimeError("prepared executable changed")
        return scope

    def remember(self, role, child):
        self.children[role] = child
        receipt = (
            json.loads(self.process_path.read_text())
            if self.process_path.exists()
            else {}
        )
        identity = process_identity(child.pid)
        if identity is None:
            raise RuntimeError(role + " exited before identity receipt")
        receipt[role] = {"pid": child.pid, "identity": identity}
        save(self.process_path, receipt)
        self.children[role] = child

    def stop(self, role):
        receipt = (
            json.loads(self.process_path.read_text())
            if self.process_path.exists()
            else {}
        )
        child = self.children.get(role)
        if child is not None:
            # Reap our children while stop_exact polls so zombies are not live work.
            threading.Thread(target=child.wait, daemon=True).start()
        owned = receipt.get(role)
        if owned is None and child is not None and child.poll() is None:
            owned = {"pid": child.pid, "identity": process_identity(child.pid)}
        stop_exact(owned)

    def cleanup(self):
        self.checked_scope()
        # Whole process shutdown drains all in-process workers before cloud deletion.
        for role in ["runner", "tunnel", "proxy"]:
            self.stop(role)
        scope = json.loads(self.scope_path.read_text())
        self.invoke("cleanup", {"LOOMPLANE_SHARED_LIVE_QUIESCED": scope["run_id"]})
        print(
            "Compute and DNS cleanup verified; DNS baseline restored. Receipted DigitalOcean tags are preserved as account metadata."
        )

    def supervise(self, child, deadline):
        while child.poll() is None:
            if time.time() >= deadline:
                raise RuntimeError("approved six-hour deadline reached")
            callback = json.loads((self.directory / "callback.json").read_text())
            if time.time() >= callback["deadline"]:
                self.stop("tunnel")
                self.stop("proxy")
            state = json.loads(self.path.read_text())
            if state.get("evidence", {}).get("enrollment_complete"):
                self.stop("tunnel")
                self.stop("proxy")
            time.sleep(0.5)
        if child.returncode:
            raise RuntimeError("live acceptance failed; evidence retained")

    def run(self, approval):
        scope = self.checked_scope(execution=True)
        if approval != digest(self.scope_path):
            raise RuntimeError(
                "run requires explicit approval of the exact prepared scope hash"
            )
        if self.process_path.exists():
            raise RuntimeError(
                "run already attempted; inspect or cleanup, never recreate"
            )
        deadline = time.time() + HOURS * 3600
        save(
            self.directory / "deadline.json",
            {"deadline": deadline, "run_id": scope["run_id"]},
        )
        save(self.process_path, {})

        def interrupted(_signal, _frame):
            raise KeyboardInterrupt

        previous = signal.signal(signal.SIGTERM, interrupted)
        try:
            with socket.socket() as probe:
                probe.bind(("127.0.0.1", 0))
                port = probe.getsockname()[1]
            proxy = subprocess.Popen(
                [
                    sys.executable,
                    str(HERE / "enrollment_proxy.py"),
                    "--policy",
                    str(self.directory / "enrollment.json"),
                    "--port",
                    str(port),
                ],
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
                start_new_session=True,
            )
            self.remember("proxy", proxy)
            callback_deadline = time.time() + 45 * 60
            tunnel = subprocess.Popen(
                [
                    str(BINARY),
                    "tunnel",
                    "--no-autoupdate",
                    "--url",
                    "http://127.0.0.1:" + str(port),
                ],
                stdout=subprocess.PIPE,
                stderr=subprocess.STDOUT,
                text=True,
                start_new_session=True,
            )
            self.remember("tunnel", tunnel)
            callback = []

            def read_tunnel():
                for line in tunnel.stdout:
                    match = re.search(r"https://[a-z0-9-]+\.trycloudflare\.com", line)
                    if match and not callback:
                        callback.append(match.group())

            threading.Thread(target=read_tunnel, daemon=True).start()
            until = time.time() + 60
            while not callback:
                if time.time() > until or tunnel.poll() is not None:
                    raise RuntimeError(
                        "approved tunnel did not publish a callback origin"
                    )
                time.sleep(0.1)
            save(
                self.directory / "callback.json",
                {
                    "origin": callback[0],
                    "deadline": min(deadline, callback_deadline),
                },
            )
            child = self.invoke(
                "run",
                {
                    "LOOMPLANE_SHARED_LIVE_APPROVAL": scope["run_id"],
                    "LOOMPLANE_SHARED_LIVE_DEADLINE": datetime.datetime.fromtimestamp(
                        deadline, datetime.UTC
                    ).isoformat(),
                    "LOOMPLANE_SHARED_LIVE_CALLBACK": callback[0],
                },
                wait=False,
            )
            self.remember("runner", child)
            self.supervise(child, deadline - 5 * 60)
            evidence = json.loads(self.path.read_text()).get("evidence", {})
            if any(
                evidence.get(key) != "passed"
                for key in [
                    "three_project_public_https",
                    "current_release_migration",
                    "future_release",
                ]
            ):
                raise RuntimeError("live command ended without required evidence")
            print("Live assertions passed; cleanup follows.")
        finally:
            signal.signal(signal.SIGTERM, previous)
            self.cleanup()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=["prepare", "run", "inspect", "cleanup"])
    parser.add_argument("--state", type=Path, required=True)
    parser.add_argument("--approve-scope")
    args = parser.parse_args()
    live = Live(args.state)
    live.directory.mkdir(parents=True, mode=0o700, exist_ok=True)
    lock = (live.directory / ".command.lock").open("a")
    try:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError:
        raise RuntimeError(
            "another command owns this run; stop that supervisor before independent cleanup"
        ) from None
    if args.command == "prepare":
        live.prepare()
    elif args.command == "run":
        live.run(args.approve_scope)
    elif args.command == "cleanup":
        live.cleanup()
    else:
        live.checked_scope()
        live.invoke("inspect")
        state = json.loads(live.path.read_text())
        print(
            json.dumps(
                {
                    key: state[key]
                    for key in ["run_id", "deadline", "hosts", "records", "cleaned"]
                },
                indent=2,
            )
        )


if __name__ == "__main__":
    try:
        main()
    except (RuntimeError, KeyboardInterrupt) as error:
        print(
            str(error) or "Interrupted; inspect the retained ledger.", file=sys.stderr
        )
        sys.exit(1)
