"""Exercise synthetic fixture isolation, bootstrap, retries and bounded load."""

import concurrent.futures
import json
import socket
import subprocess
import sys
import tempfile
import threading
import unittest
from pathlib import Path
from urllib.error import HTTPError
from urllib.request import Request, urlopen

import fixture


class WorkloadTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.a = fixture.Store(Path(self.directory.name), "instance-a")
        self.b = fixture.Store(Path(self.directory.name), "instance-b")

    def start_server(self, role="api"):
        server = fixture.server(self.a, role, "token-a", port=0)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        return server

    def test_idle_connections_are_bounded(self):
        server = self.start_server()
        sockets = []
        try:
            for _ in range(8):
                connection = socket.create_connection(server.server_address, timeout=1)
                connection.sendall(b"GET /health HTTP/1.1\r\n")
                sockets.append(connection)
            with socket.create_connection(server.server_address, timeout=1) as extra:
                try:
                    self.assertEqual(extra.recv(1), b"")
                except ConnectionResetError:
                    pass
        finally:
            for connection in sockets:
                connection.close()

    def test_idle_connection_reaches_deadline(self):
        server = self.start_server()
        with socket.create_connection(server.server_address, timeout=3) as idle:
            idle.sendall(b"GET /health HTTP/1.1\r\n")
            self.assertEqual(idle.recv(1), b"")

    def test_empty_state_directory_is_rejected_before_files_are_created(self):
        script = Path(fixture.__file__).resolve()
        result = subprocess.run(
            [
                sys.executable,
                str(script),
                "--role",
                "bootstrap",
                "--instance",
                "instance-a",
                "--state-dir",
                "",
            ],
            cwd=self.directory.name,
            capture_output=True,
            text=True,
            check=False,
            timeout=5,
        )
        self.assertEqual(result.returncode, 2)
        self.assertEqual(list(Path(self.directory.name).iterdir()), [])

    def test_corrupt_storage_returns_503_for_health_and_metrics(self):
        self.a.path.write_bytes(b"invalid database")
        for role, route in (("api", "/health"), ("sidecar", "/metrics")):
            server = self.start_server(role)
            request = Request(
                "http://127.0.0.1:" + str(server.server_port) + route,
                headers={"Authorization": "Bearer token-a"},
            )
            with self.assertRaises(HTTPError) as error:
                urlopen(request, timeout=3)
            self.assertEqual(error.exception.code, 503)
            self.assertEqual(json.load(error.exception), {"error": "store_unavailable"})
            error.exception.close()

    def test_bootstrap_is_required_and_repeatable_without_erasing_data(self):
        self.assertFalse(self.a.ready())
        with self.assertRaises(ValueError):
            self.a.put("data", "sample", "before-bootstrap")
        self.a.bootstrap()
        self.a.put("data", "sample", "keep")
        self.a.bootstrap()
        self.assertTrue(self.a.ready())
        self.assertEqual(self.a.get("data", "sample"), "keep")
        self.assertFalse(self.b.ready())

    def test_database_and_bucket_names_do_not_share_values_between_instances(self):
        for store in (self.a, self.b):
            store.bootstrap()
        for kind in ("data", "bucket"):
            self.a.put(kind, "sample", "alpha")
            self.b.put(kind, "sample", "beta")
            self.assertEqual(self.a.get(kind, "sample"), "alpha")
            self.assertEqual(self.b.get(kind, "sample"), "beta")

    def test_worker_restart_and_duplicate_job_do_not_repeat_completed_work(self):
        self.a.bootstrap()
        self.a.enqueue("job-1", "sample")
        self.a.enqueue("job-1", "sample")
        with self.assertRaises(ValueError):
            self.a.enqueue("job-1", "changed")
        restarted = fixture.Store(Path(self.directory.name), "instance-a")
        self.assertEqual(restarted.metrics()["queue_depth"], 1)
        with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
            list(pool.map(lambda _: restarted.work(), range(2)))
        self.assertEqual(restarted.metrics()["completed_jobs"], 1)
        self.assertEqual(restarted.metrics()["queue_depth"], 0)
        self.assertEqual(restarted.work(), 0)

    def test_queue_storage_and_payload_bounds(self):
        self.a.bootstrap()
        for i in range(32):
            self.a.enqueue("job-" + str(i), "sample")
        with self.assertRaises(ValueError):
            self.a.enqueue("overflow", "sample")
        self.a.work()
        # Bound total retained metadata too, not just pending work.
        with self.assertRaises(ValueError):
            self.a.enqueue("after-drain", "sample")
        with self.assertRaises(ValueError):
            self.a.put("bucket", "sample", "x" * 4097)
        with self.assertRaises(ValueError):
            fixture.Store(Path(self.directory.name), "../outside")

    def test_http_rejects_neighbor_credential_and_requires_bootstrap(self):
        server = fixture.server(self.a, "api", "token-a", port=0)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        base = "http://127.0.0.1:" + str(server.server_port)

        def request(path, token, value=None):
            data = json.dumps(value).encode() if value is not None else None
            req = Request(
                base + path,
                data=data,
                headers={"Authorization": "Bearer " + token},
                method="PUT" if value is not None else "GET",
            )
            with urlopen(req, timeout=3) as response:
                return json.load(response)

        with self.assertRaises(HTTPError) as error:
            request("/health", "token-a")
        self.assertEqual(error.exception.code, 503)
        error.exception.close()
        self.a.bootstrap()
        self.assertTrue(request("/health", "token-a")["ready"])
        with self.assertRaises(HTTPError) as error:
            request("/data/sample", "token-b", {"value": "neighbor"})
        self.assertEqual(error.exception.code, 403)
        error.exception.close()
        request("/data/sample", "token-a", {"value": "owned"})
        self.assertEqual(request("/data/sample", "token-a")["value"], "owned")

    def test_sidecar_and_control_cannot_write_instance_data(self):
        self.a.bootstrap()
        for role in ("sidecar", "control"):
            server = fixture.server(self.a, role, "token-a", port=0)
            thread = threading.Thread(target=server.serve_forever, daemon=True)
            thread.start()
            try:
                req = Request(
                    "http://127.0.0.1:" + str(server.server_port) + "/data/sample",
                    data=b'{"value":"wrong-role"}',
                    method="PUT",
                    headers={"Authorization": "Bearer token-a"},
                )
                with self.assertRaises(HTTPError) as error:
                    urlopen(req, timeout=3)
                self.assertEqual(error.exception.code, 404)
                error.exception.close()
            finally:
                server.shutdown()
                server.server_close()
            self.assertIsNone(self.a.get("data", "sample"))


if __name__ == "__main__":
    unittest.main()
