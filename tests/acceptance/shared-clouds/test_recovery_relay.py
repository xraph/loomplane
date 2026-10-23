"""Exercise the exact acceptance relay with real sockets and failing observers."""

import io
import json
import re
import socket
import threading
import unittest
from pathlib import Path
from unittest.mock import patch


class RelayTransparencyTests(unittest.TestCase):
    def setUp(self):
        fixture = (
            Path(__file__).parents[2]
            / "integration/shared_cloud_recovery_fixture_test.go"
        )
        source = re.search(
            r"const recoveryRelay = `(.*?)`\n", fixture.read_text(), re.DOTALL
        ).group(1)
        self.relay = {"__name__": "relay_regression"}
        exec(compile(source, str(fixture), "exec"), self.relay)

    def exchange(self, observe, request, response, continuation=b""):
        client, incoming = socket.socketpair()
        outgoing, daemon = socket.socketpair()
        for stream in (client, incoming, outgoing, daemon):
            stream.settimeout(2)
            self.addCleanup(stream.close)
        worker = threading.Thread(
            target=self.relay["forward"],
            args=(incoming, outgoing, observe),
            daemon=True,
        )
        worker.start()

        def receive_exact(stream, size):
            received = b""
            while len(received) < size:
                part = stream.recv(size - len(received))
                self.assertTrue(part, "relay truncated bytes before EOF")
                received += part
            return received

        client.sendall(request)
        self.assertEqual(receive_exact(daemon, len(request)), request)
        if continuation:
            client.sendall(continuation)
            self.assertEqual(receive_exact(daemon, len(continuation)), continuation)
        client.shutdown(socket.SHUT_WR)
        self.assertEqual(daemon.recv(1), b"", "client half-close was not forwarded")
        # The response is deliberately sent only after the client write EOF.
        daemon.sendall(response)
        daemon.shutdown(socket.SHUT_WR)
        self.assertEqual(receive_exact(client, len(response)), response)
        self.assertEqual(client.recv(1), b"", "daemon half-close was not forwarded")
        worker.join(2)
        self.assertFalse(worker.is_alive(), "relay did not finish after both EOFs")

    def assert_error_summary(self, kind):
        summary = self.relay["observation_summary"]()
        self.assertEqual(summary, {"error_count": 1, "errors": [kind]})
        output = io.StringIO()
        # Retain the exact final receipt the qualification reads after teardown.
        with patch("builtins.open") as opened:
            opened.return_value.__enter__.return_value = output
            with self.assertRaises(SystemExit):
                self.relay["finish"](0, None)
            opened.assert_called_once_with(
                "/tmp/loomplane-recovery-observation.json", "w"
            )
        self.assertEqual(json.loads(output.getvalue()), summary)
        self.assertNotIn("fixture-secret", output.getvalue())

    @staticmethod
    def stop_request():
        return (
            b"POST /containers/" + b"a" * 64 + b"/stop?t=10 HTTP/1.1\r\n"
            b"Authorization: Bearer fixture-secret\r\nContent-Length: 0\r\n\r\n"
        )

    def test_missing_helper_proc_preserves_response_and_half_close(self):
        self.exchange(
            self.relay["observer"](1 << 30),
            self.stop_request(),
            b"HTTP/1.1 304 Not Modified\r\nContent-Length: 0\r\n\r\n",
            b"GET /info HTTP/1.1\r\n\r\n",
        )
        self.assert_error_summary("FileNotFoundError")

    def test_receipt_write_failure_preserves_error_status_and_half_close(self):
        # PID 1 has no ancestry to inspect, so this reaches the exact receipt writer.
        with patch.object(
            self.relay["os"], "open", side_effect=PermissionError("fixture-secret")
        ):
            self.exchange(
                self.relay["observer"](1),
                self.stop_request(),
                b"HTTP/1.1 500 Internal Server Error\r\nContent-Length: 4\r\n\r\noops",
                b"GET /info HTTP/1.1\r\n\r\n",
            )
        self.assert_error_summary("PermissionError")

    def test_upgrade_preserves_binary_traffic_and_half_close(self):
        self.exchange(
            self.relay["observer"](1 << 30),
            b"POST /attach HTTP/1.1\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n",
            b"HTTP/1.1 101 Switching Protocols\r\nUpgrade: tcp\r\n\r\n"
            b"\x00\xffoutput\r\n",
            b"\x00\xffinput\r\n\r\n",
        )
        self.assertEqual(
            self.relay["observation_summary"](), {"error_count": 0, "errors": []}
        )
