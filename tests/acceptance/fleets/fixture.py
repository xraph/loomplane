#!/usr/bin/env python3
"""Run synthetic control, API, sidecar, bootstrap and worker roles locally."""

import argparse
import hashlib
import hmac
import json
import os
import sqlite3
import threading
from contextlib import contextmanager
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

from run import identity


class Store:
    """SQLite adapters stand in for separately credentialed managed data services."""

    def __init__(self, directory, instance):
        if instance not in ("instance-a", "instance-b"):
            raise ValueError("unknown synthetic instance")
        self.instance = instance
        directory = Path(directory)
        directory.mkdir(parents=True, exist_ok=True)
        self.path = directory / (instance + ".sqlite3")

    @contextmanager
    def connection(self):
        connection = sqlite3.connect(self.path, timeout=5)
        try:
            with connection:
                yield connection
        finally:
            connection.close()

    def ready(self):
        if not self.path.exists():
            return False
        with self.connection() as connection:
            return connection.execute("PRAGMA user_version").fetchone()[0] == 1

    def bootstrap(self):
        with self.connection() as connection:
            connection.execute("BEGIN IMMEDIATE")
            version = connection.execute("PRAGMA user_version").fetchone()[0]
            if version not in (0, 1):
                raise ValueError("unsupported schema version")
            connection.execute(
                "CREATE TABLE IF NOT EXISTS values_store (kind TEXT, key TEXT, value TEXT, PRIMARY KEY(kind,key))"
            )
            connection.execute(
                "CREATE TABLE IF NOT EXISTS jobs (id TEXT PRIMARY KEY, value TEXT, result TEXT)"
            )
            connection.execute("PRAGMA user_version = 1")

    def require_ready(self):
        if not self.ready():
            raise ValueError("bootstrap required")

    def validate_value(self, key, value):
        identity(key, "key")
        if not isinstance(value, str) or len(value.encode()) > 4096:
            raise ValueError("value exceeds 4096 bytes")

    def put(self, kind, key, value):
        self.require_ready()
        self.validate_value(key, value)
        if kind not in ("data", "bucket"):
            raise ValueError("unknown adapter")
        with self.connection() as connection:
            connection.execute("BEGIN IMMEDIATE")
            existing = connection.execute(
                "SELECT 1 FROM values_store WHERE kind=? AND key=?", (kind, key)
            ).fetchone()
            count = connection.execute("SELECT COUNT(*) FROM values_store").fetchone()[
                0
            ]
            if existing is None and count >= 64:
                raise ValueError("fixture storage limit reached")
            connection.execute(
                "INSERT INTO values_store VALUES(?,?,?) ON CONFLICT(kind,key) DO UPDATE SET value=excluded.value",
                (kind, key, value),
            )

    def get(self, kind, key):
        self.require_ready()
        identity(key, "key")
        if kind not in ("data", "bucket"):
            raise ValueError("unknown adapter")
        with self.connection() as connection:
            result = connection.execute(
                "SELECT value FROM values_store WHERE kind=? AND key=?", (kind, key)
            ).fetchone()
            return result[0] if result else None

    def enqueue(self, job_id, value):
        self.require_ready()
        self.validate_value(job_id, value)
        with self.connection() as connection:
            connection.execute("BEGIN IMMEDIATE")
            previous = connection.execute(
                "SELECT value FROM jobs WHERE id=?", (job_id,)
            ).fetchone()
            if previous:
                if previous[0] != value:
                    raise ValueError("job identity conflict")
                return
            if connection.execute("SELECT COUNT(*) FROM jobs").fetchone()[0] >= 32:
                raise ValueError("fixture queue limit reached")
            connection.execute("INSERT INTO jobs VALUES(?,?,NULL)", (job_id, value))

    def work(self):
        self.require_ready()
        completed = 0
        with self.connection() as connection:
            # A crash rolls back both the claim and result; another worker retries.
            connection.execute("BEGIN IMMEDIATE")
            jobs = connection.execute(
                "SELECT id,value FROM jobs WHERE result IS NULL ORDER BY id LIMIT 32"
            ).fetchall()
            for job_id, value in jobs:
                result = value.encode()
                for _ in range(1000):
                    result = hashlib.sha256(result).digest()
                connection.execute(
                    "UPDATE jobs SET result=? WHERE id=?", (result.hex(), job_id)
                )
                completed += 1
        return completed

    def metrics(self):
        self.require_ready()
        with self.connection() as connection:
            queued = connection.execute(
                "SELECT COUNT(*) FROM jobs WHERE result IS NULL"
            ).fetchone()[0]
            completed = connection.execute(
                "SELECT COUNT(*) FROM jobs WHERE result IS NOT NULL"
            ).fetchone()[0]
        return {"queue_depth": queued, "completed_jobs": completed}


class BoundedHTTPServer(ThreadingHTTPServer):
    daemon_threads = True

    def __init__(self, address, handler):
        self.slots = threading.BoundedSemaphore(8)
        super().__init__(address, handler)

    def get_request(self):
        request, address = super().get_request()
        request.settimeout(2)
        return request, address

    def process_request(self, request, address):
        if not self.slots.acquire(blocking=False):
            self.shutdown_request(request)
            return
        try:
            super().process_request(request, address)
        except BaseException:
            self.slots.release()
            raise

    def process_request_thread(self, request, address):
        try:
            super().process_request_thread(request, address)
        finally:
            self.slots.release()


def server(store, role, token, port=8080):
    if role not in ("api", "sidecar", "control") or not token:
        raise ValueError("server role and synthetic credential are required")

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *_args):
            pass

        def respond(self, code, value):
            body = json.dumps(value).encode()
            self.send_response(code)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def authorized(self):
            provided = self.headers.get("Authorization", "")
            return hmac.compare_digest(provided.encode(), ("Bearer " + token).encode())

        def dispatch(self, write=False):
            try:
                self.dispatch_request(write)
            except sqlite3.Error:
                self.respond(503, {"error": "store_unavailable"})
            except OSError:
                self.close_connection = True

        def dispatch_request(self, write=False):
            if not self.authorized():
                self.respond(403, {"error": "denied"})
                return
            if not write and self.path == "/health":
                ready = role == "control" or store.ready()
                self.respond(200 if ready else 503, {"ready": ready, "role": role})
                return
            if role == "control":
                if not write and self.path == "/instances":
                    self.respond(200, {"instances": ["instance-a", "instance-b"]})
                else:
                    self.respond(404, {"error": "not_found"})
                return
            if role == "sidecar":
                if not write and self.path == "/metrics" and store.ready():
                    self.respond(200, store.metrics())
                else:
                    self.respond(404, {"error": "not_found"})
                return
            try:
                parts = self.path.split("/")
                if len(parts) != 3 or parts[1] not in ("data", "bucket", "jobs"):
                    self.respond(404, {"error": "not_found"})
                    return
                kind, key = parts[1:]
                identity(key, "key")
                if write:
                    size = int(self.headers.get("Content-Length", "0"))
                    if not 0 < size <= 8192 or self.headers.get("Transfer-Encoding"):
                        raise ValueError("invalid body size")
                    value = json.loads(self.rfile.read(size))
                    if not isinstance(value, dict) or set(value) != {"value"}:
                        raise ValueError("invalid body")
                    if kind == "jobs":
                        store.enqueue(key, value["value"])
                    else:
                        store.put(kind, key, value["value"])
                    self.respond(200, {"accepted": True})
                elif kind == "jobs":
                    self.respond(404, {"error": "not_found"})
                else:
                    self.respond(200, {"value": store.get(kind, key)})
            except (ValueError, TypeError):
                self.respond(422, {"error": "invalid_or_unready"})
            except sqlite3.Error:
                self.respond(503, {"error": "store_unavailable"})

        def do_GET(self):
            self.dispatch()

        def do_PUT(self):
            self.dispatch(write=True)

    result = BoundedHTTPServer(("127.0.0.1", port), Handler)
    return result


def state_directory(value):
    if not value.strip():
        raise argparse.ArgumentTypeError("state directory must not be empty")
    return Path(value)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--role",
        choices=("control", "api", "sidecar", "bootstrap", "worker"),
        required=True,
    )
    parser.add_argument(
        "--instance", choices=("instance-a", "instance-b"), required=True
    )
    parser.add_argument("--state-dir", type=state_directory, required=True)
    parser.add_argument("--port", type=int, default=8080)
    args = parser.parse_args()
    store = Store(args.state_dir, args.instance)
    if args.role == "bootstrap":
        store.bootstrap()
        print(json.dumps({"ready": store.ready()}))
    elif args.role == "worker":
        print(json.dumps({"completed": store.work(), **store.metrics()}))
    else:
        token = os.environ.get("LOOMPLANE_FLEET_FIXTURE_TOKEN")
        if not token:
            parser.error("LOOMPLANE_FLEET_FIXTURE_TOKEN is required")
        http = server(store, args.role, token, port=args.port)
        try:
            http.serve_forever()
        finally:
            http.server_close()


if __name__ == "__main__":
    main()
