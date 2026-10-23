import http.client
import http.server
import json
import socket
import tempfile
import threading
import time
import unittest
from pathlib import Path
from unittest.mock import patch

from enrollment_proxy import LIMIT, EnrollmentProxy


class EnrollmentProxyTests(unittest.TestCase):
    def setUp(self):
        self.calls = []
        calls = self.calls

        class Upstream(http.server.BaseHTTPRequestHandler):
            def log_message(self, *_args):
                pass

            def do_POST(self):
                calls.append(
                    (self.path, self.rfile.read(int(self.headers["Content-Length"])))
                )
                self.send_response(204)
                self.end_headers()

        self.upstream = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Upstream)
        self.directory = tempfile.TemporaryDirectory()
        self.policy = Path(self.directory.name) / "policy.json"
        self.path = "/api/v1/infrastructure/enroll/org_1/op_2"
        self.update([])
        self.proxy = EnrollmentProxy(("127.0.0.1", 0), self.policy)
        for server in [self.upstream, self.proxy]:
            threading.Thread(target=server.serve_forever, daemon=True).start()
            self.addCleanup(server.server_close)
            self.addCleanup(server.shutdown)
        self.addCleanup(self.directory.cleanup)

    def update(self, paths, **kwargs):
        value = {
            "paths": paths,
            "deadline": time.time() + 60,
            "upstream": "http://127.0.0.1:" + str(self.upstream.server_port),
        }
        value.update(kwargs)
        temporary = self.policy.with_suffix(".tmp")
        temporary.write_text(json.dumps(value))
        temporary.replace(self.policy)

    def request(self, method="POST", path=None, body=b"{}", headers=None):
        connection = http.client.HTTPConnection(
            "127.0.0.1", self.proxy.server_port, timeout=3
        )
        try:
            connection.request(method, path or self.path, body, headers or {})
            return connection.getresponse().status
        finally:
            connection.close()

    def test_empty_then_exact_atomic_enrollment(self):
        self.assertEqual(self.request(), 403)
        self.update([self.path])
        self.assertEqual(self.request(), 204)
        self.update([])
        self.assertEqual(self.request(), 403)
        self.assertEqual(self.calls, [(self.path, b"{}")])

    def test_denied_requests_never_forward(self):
        self.update([self.path])
        for method in [
            "GET",
            "PUT",
            "PATCH",
            "DELETE",
            "OPTIONS",
            "HEAD",
            "TRACE",
            "CONNECT",
        ]:
            self.assertEqual(self.request(method=method), 403)
        for path in [
            self.path + "?x=1",
            self.path + "/",
            self.path.replace("org_1", "org_3"),
            self.path.replace("org_1", "%6frg_1"),
            self.path + "/../op_2",
            "/api/v1/connections",
            "http://example.com" + self.path,
            "//api/v1/infrastructure/enroll/org_1/op_2",
        ]:
            self.assertEqual(self.request(path=path), 403)
        self.assertEqual(self.request(body=b"x" * (LIMIT + 1)), 403)
        self.assertEqual(self.request(headers={"Transfer-Encoding": "chunked"}), 403)
        self.assertEqual(self.calls, [])

    def test_primary_expired_invalid_policy_denied(self):
        for policy in [
            {"upstream": "http://127.0.0.1:8780"},
            {"upstream": "http://localhost:1234"},
            {"upstream": "https://127.0.0.1:1234"},
            {"deadline": time.time() - 1},
        ]:
            self.update([self.path], **policy)
            self.assertEqual(self.request(), 403)
        self.policy.write_text("{")
        self.assertEqual(self.request(), 503)
        self.assertEqual(self.calls, [])

    def test_duplicate_length_and_incomplete_body_never_forward(self):
        self.update([self.path])
        for headers in [
            "Content-Length: 2\r\nContent-Length: 2\r\n",
            "Content-Length: 10\r\n",
        ]:
            with socket.create_connection(
                ("127.0.0.1", self.proxy.server_port)
            ) as connection:
                connection.sendall(
                    (
                        "POST "
                        + self.path
                        + " HTTP/1.1\r\nHost: localhost\r\n"
                        + headers
                        + "\r\n{}"
                    ).encode()
                )
                connection.shutdown(socket.SHUT_WR)
                connection.recv(1024)
        self.assertEqual(self.calls, [])

    def test_slow_body_expires_without_forwarding(self):
        self.update([self.path])
        with (
            patch("enrollment_proxy.DURATION", 0.15),
            socket.create_connection(
                ("127.0.0.1", self.proxy.server_port)
            ) as connection,
        ):
            connection.settimeout(1)
            connection.sendall(
                (
                    "POST "
                    + self.path
                    + " HTTP/1.1\r\nHost: localhost\r\nContent-Length: 10\r\n\r\n"
                ).encode()
            )
            started = time.monotonic()
            reply = connection.recv(1024)
            self.assertTrue(
                not reply or reply.startswith((b"HTTP/1.0 503", b"HTTP/1.0 400"))
            )
            self.assertLess(time.monotonic() - started, 0.8)
        self.assertEqual(self.calls, [])
