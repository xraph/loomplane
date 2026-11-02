"""Expose only persisted enrollment paths to an isolated loopback app."""

import argparse
import http.client
import http.server
import json
import re
import socket
import time
from pathlib import Path
from urllib.parse import urlsplit

LIMIT = 16 * 1024
DURATION = 10
PATH = re.compile(r"/api/v1/infrastructure/enroll/[a-zA-Z0-9_-]+/[a-zA-Z0-9_-]+\Z")


class EnrollmentProxy(http.server.ThreadingHTTPServer):
    daemon_threads = True

    def __init__(self, address, policy):
        self.policy = Path(policy)
        super().__init__(address, Handler)


class Handler(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.0"

    def log_message(self, *_args):
        pass

    def setup(self):
        super().setup()
        self.connection.settimeout(DURATION)

    def reject(self, status=403):
        try:
            self.send_response(status)
            self.send_header("Content-Length", "0")
            self.end_headers()
        except (OSError, ValueError):
            pass
        self.close_connection = True

    def parse_request(self):
        if not super().parse_request():
            return False
        target = self.raw_requestline.split()[1].decode("iso-8859-1")
        if target != self.path:
            self.reject()
            return False
        return True

    def handle_one_request(self):
        # Bound headers and body together, including slow clients.
        import threading

        self.expires = time.monotonic() + DURATION

        def expire():
            try:
                self.connection.shutdown(socket.SHUT_RDWR)
            except OSError:
                pass
            self.connection.close()

        timer = threading.Timer(DURATION, expire)
        timer.daemon = True
        timer.start()
        try:
            super().handle_one_request()
        finally:
            timer.cancel()

    def do_POST(self):
        try:
            policy = json.loads(self.server.policy.read_text())
            upstream = urlsplit(policy.get("upstream", ""))
            paths = policy.get("paths", [])
            valid = (
                PATH.fullmatch(self.path)
                and self.path in paths
                and all(PATH.fullmatch(path) for path in paths)
                and time.time() < policy["deadline"]
                and upstream.scheme == "http"
                and upstream.hostname == "127.0.0.1"
                and upstream.port not in (None, 8780)
                and not upstream.path
                and not upstream.query
                and not upstream.fragment
                and not upstream.username
                and not self.headers.get_all("Transfer-Encoding")
                and len(self.headers.get_all("Content-Length", [])) == 1
            )
            length = int(self.headers.get("Content-Length", "-1"))
            if not valid or length < 0 or length > LIMIT:
                self.reject()
                return
            body = self.rfile.read(length)
            if len(body) != length:
                self.reject(400)
                return
            connection = http.client.HTTPConnection(
                upstream.hostname,
                upstream.port,
                timeout=max(0.01, self.expires - time.monotonic()),
            )
            try:
                connection.request(
                    "POST",
                    self.path,
                    body,
                    {
                        "Content-Type": "application/json",
                        "Authorization": self.headers.get("Authorization", ""),
                    },
                )
                response = connection.getresponse()
                # Enrollment has no useful public response payload.
                self.reject(response.status if response.status == 204 else 502)
            finally:
                connection.close()
        except (OSError, ValueError, KeyError, http.client.HTTPException):
            self.reject(503)

    def do_GET(self):
        self.reject()

    do_HEAD = do_GET
    do_PUT = do_GET
    do_PATCH = do_GET
    do_DELETE = do_GET
    do_OPTIONS = do_GET
    do_TRACE = do_GET
    do_CONNECT = do_GET


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--policy", required=True)
    parser.add_argument("--port", type=int, required=True)
    arguments = parser.parse_args()
    EnrollmentProxy(("127.0.0.1", arguments.port), arguments.policy).serve_forever()
