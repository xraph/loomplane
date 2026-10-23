"""Check private fixture boundaries and resumable cleanup without Docker."""

import datetime
import http.client
import importlib.util
import json
import socket
import socketserver
import struct
import subprocess
import tempfile
import threading
import unittest
from http.server import ThreadingHTTPServer
from pathlib import Path
from unittest.mock import Mock, patch


def module(name):
    spec = importlib.util.spec_from_file_location(
        name, Path(__file__).with_name(name + ".py")
    )
    result = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(result)
    return result


runner = module("run")
fixture = module("fixture")


def query(name, kind=1):
    labels = b"".join(bytes([len(label)]) + label.encode() for label in name.split("."))
    return (
        struct.pack("!HHHHHH", 42, 256, 1, 0, 0, 0)
        + labels
        + b"\0"
        + struct.pack("!HH", kind, 1)
    )


class DNSBoundaryTests(unittest.TestCase):
    def test_only_pinned_acme_hosts_are_trapped(self):
        for host in fixture.ACME_HOSTS:
            response = fixture.answer(query(host), {}, "172.20.0.10")
            self.assertEqual(response[-4:], bytes([172, 20, 0, 10]))
            self.assertEqual(struct.unpack("!H", response[6:8])[0], 1)
        unknown = fixture.answer(query("acme.example.com"), {}, "172.20.0.10")
        self.assertEqual(unknown[3] & 15, 3)

    def test_tcp_dns_uses_real_framing_and_preserves_exact_records(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "records.json").write_text(
                json.dumps({"app.fixture.test": {"A": ["172.20.0.21"]}})
            )
            with (
                patch.object(fixture, "DIRECTORY", root),
                patch.object(
                    fixture, "CONFIG", {"gateway": "172.20.0.10"}, create=True
                ),
                socketserver.ThreadingTCPServer(
                    ("127.0.0.1", 0), fixture.DNSTCP
                ) as server,
            ):
                thread = threading.Thread(target=server.serve_forever, daemon=True)
                thread.start()
                try:
                    with socket.create_connection(
                        server.server_address, timeout=2
                    ) as connection:
                        stream = connection.makefile("rwb")
                        for name in ("app.fixture.test", "unknown.test"):
                            packet = query(name)
                            stream.write(struct.pack("!H", len(packet)) + packet)
                            stream.flush()
                            size = struct.unpack("!H", stream.read(2))[0]
                            response = stream.read(size)
                            if name == "app.fixture.test":
                                self.assertEqual(response[-4:], bytes([172, 20, 0, 21]))
                            else:
                                self.assertEqual(response[3] & 15, 3)
                        stream.close()
                finally:
                    server.shutdown()
                    thread.join()

    def test_dns_records_do_not_fabricate_ipv6(self):
        records = {"app.fixture.test": {"A": ["172.20.0.21"], "TXT": ["proof"]}}
        self.assertEqual(
            fixture.answer(query("app.fixture.test"), records, "172.20.0.10")[-4:],
            bytes([172, 20, 0, 21]),
        )
        self.assertEqual(
            fixture.answer(query("app.fixture.test", 16), records, "172.20.0.10")[-6:],
            b"\x05proof",
        )
        self.assertEqual(
            struct.unpack(
                "!H",
                fixture.answer(query("app.fixture.test", 28), records, "172.20.0.10")[
                    6:8
                ],
            )[0],
            0,
        )


class DNSReceiptTests(unittest.TestCase):
    def test_receipt_timestamp_brackets_actual_record_change(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "records.json").write_text("{}")
            with (
                patch.object(fixture, "DIRECTORY", root),
                ThreadingHTTPServer(("127.0.0.1", 0), fixture.Control) as server,
            ):
                thread = threading.Thread(target=server.serve_forever, daemon=True)
                thread.start()
                connection = http.client.HTTPConnection(
                    *server.server_address, timeout=2
                )
                try:
                    before = datetime.datetime.now(datetime.UTC)
                    connection.request(
                        "POST",
                        "/records",
                        json.dumps(
                            {"name": "app.test", "values": {"A": ["172.20.0.22"]}}
                        ),
                    )
                    response = connection.getresponse()
                    changed_at = datetime.datetime.fromisoformat(
                        response.getheader("X-Fixture-Changed-At")
                    )
                    after = datetime.datetime.now(datetime.UTC)
                    self.assertEqual(response.status, 204)
                    self.assertLessEqual(before, changed_at)
                    self.assertLessEqual(changed_at, after)
                    self.assertEqual(
                        json.loads((root / "records.json").read_text()),
                        {"app.test": {"A": ["172.20.0.22"]}},
                    )
                finally:
                    connection.close()
                    server.shutdown()
                    thread.join()


class GatewayBoundaryTests(unittest.TestCase):
    def test_unknown_hosts_management_paths_and_signed_directory_fail_closed(self):
        for host, path, method in (
            ("unknown.test", "/directory", "GET"),
            ("acme-v02.api.letsencrypt.org", "/roots/0", "GET"),
            ("acme-v02.api.letsencrypt.org", "/root-keys/0", "GET"),
            ("acme-v02.api.letsencrypt.org", "/directory", "POST"),
            ("acme-v02.api.letsencrypt.org", "/nonce-plz-extra", "GET"),
        ):
            handler = fixture.Gateway.__new__(fixture.Gateway)
            handler.headers = {"Host": host}
            handler.path = path
            handler.command = method
            handler.send_error = Mock()
            with patch.object(fixture.http.client, "HTTPSConnection") as upstream:
                handler.forward()
                handler.send_error.assert_called_once_with(403)
                upstream.assert_not_called()


class OwnershipTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.path = Path(self.tmp.name) / "state.json"
        self.fixture = runner.Fixture(self.path)
        self.fixture.state = {
            "run_id": "lp-shared-test",
            "daemon": "parent",
            "containers": [],
            "volumes": [],
            "network": None,
        }
        self.fixture.persist()

    def test_linux_binary_matches_docker_architecture(self):
        for value, expected in (
            ("aarch64", "arm64"),
            ("arm64", "arm64"),
            ("x86_64", "amd64"),
            ("amd64", "amd64"),
        ):
            self.assertEqual(runner.go_architecture(value), expected)
        with self.assertRaises(ValueError):
            runner.go_architecture("unknown")

    def test_identity_is_durable_before_start(self):
        def docker(*args, **kwargs):
            state = json.loads(self.path.read_text())
            if args[0] == "create":
                self.assertIsNone(state["containers"][0]["id"])
                return "immutable"
            self.assertEqual(args, ("start", "immutable"))
            self.assertEqual(state["containers"][0]["id"], "immutable")
            return ""

        with patch.object(self.fixture, "docker", side_effect=docker):
            self.fixture.create("gateway", "image")

    def test_cleanup_refuses_changed_daemon(self):
        with patch.object(self.fixture, "docker", return_value="different") as docker:
            with self.assertRaisesRegex(RuntimeError, "daemon changed"):
                self.fixture.cleanup()
            self.assertEqual(docker.call_count, 1)

    def test_cleanup_recovers_lost_create_response_and_is_rerunnable(self):
        self.fixture.state["containers"] = [
            {"role": "host", "name": "lp-shared-test-host", "id": None}
        ]
        self.fixture.persist()
        present = True

        def docker(*args, **kwargs):
            nonlocal present
            if args[0] == "info":
                return "parent"
            if args[:2] == ("container", "inspect"):
                if not present:
                    raise subprocess.CalledProcessError(
                        1,
                        args,
                        stderr=("Error: No such container: " + args[-1]).encode(),
                    )
                return json.dumps(
                    [
                        {
                            "Id": "exact-id",
                            "Config": {
                                "Labels": {"loomplane.acceptance": "lp-shared-test"}
                            },
                        }
                    ]
                )
            self.assertEqual(args, ("rm", "-f", "exact-id"))
            present = False
            return ""

        with patch.object(self.fixture, "docker", side_effect=docker):
            self.fixture.cleanup()
            self.fixture.cleanup()
        self.assertTrue(self.fixture.state["cleaned"])

    def test_volume_creation_refuses_existing_resource(self):
        with (
            patch.object(self.fixture, "inspect", return_value={"Name": "existing"}),
            patch.object(self.fixture, "docker") as docker,
        ):
            with self.assertRaisesRegex(RuntimeError, "already exists"):
                self.fixture.create_volume("existing")
            docker.assert_not_called()
        self.assertEqual(self.fixture.state["volumes"], [])

    def test_volume_intent_precedes_create_and_ownership_is_checked(self):
        def docker(*args, **kwargs):
            self.assertEqual(json.loads(self.path.read_text())["volumes"], ["unique"])
            return "unique"

        with (
            patch.object(self.fixture, "inspect", return_value=None),
            patch.object(self.fixture, "docker", side_effect=docker),
            patch.object(
                self.fixture,
                "owned",
                side_effect=RuntimeError("cleanup owner mismatch"),
            ),
            self.assertRaisesRegex(RuntimeError, "owner mismatch"),
        ):
            self.fixture.create_volume("unique")

    def test_cleanup_before_setup_does_not_contact_docker(self):
        self.fixture.state = {}
        with patch.object(self.fixture, "docker") as docker:
            self.fixture.cleanup()
            docker.assert_not_called()

    def test_inspection_failure_does_not_report_success(self):
        self.fixture.state["containers"] = [
            {"role": "host", "id": "exact", "name": "host"}
        ]
        for stderr in (
            b"Cannot connect to Docker daemon",
            b"permission denied",
            b"No such container: another-id",
        ):
            with (
                patch.object(
                    self.fixture,
                    "docker",
                    side_effect=[
                        "parent",
                        subprocess.CalledProcessError(1, [], stderr=stderr),
                    ],
                ),
                self.assertRaises(subprocess.CalledProcessError),
            ):
                self.fixture.cleanup()
            self.assertFalse(self.fixture.state["cleaned"])

    def test_docker_absence_signatures_are_exact(self):
        for kind, detail in (
            ("container", b"Error: No such container: exact"),
            ("volume", b"Error response from daemon: get exact: no such volume"),
            ("network", b"Error response from daemon: network exact not found"),
        ):
            with patch.object(
                self.fixture,
                "docker",
                side_effect=subprocess.CalledProcessError(1, [], stderr=detail),
            ):
                self.assertIsNone(self.fixture.inspect(kind, "exact"))
                with self.assertRaises(subprocess.CalledProcessError):
                    self.fixture.inspect(kind, "other")

    def test_nested_failure_cannot_be_ignored(self):
        self.fixture.state["hosts"] = {
            "host1": {"container": "host-id", "daemon": "nested"}
        }
        owned = json.dumps(
            [
                {
                    "Id": "host-id",
                    "Config": {"Labels": {"loomplane.acceptance": "lp-shared-test"}},
                }
            ]
        )
        with (
            patch.object(
                self.fixture,
                "docker",
                side_effect=[
                    "parent",
                    owned,
                    subprocess.CalledProcessError(1, [], stderr=b"nested unavailable"),
                ],
            ),
            self.assertRaises(subprocess.CalledProcessError),
        ):
            self.fixture.cleanup()
        self.assertFalse(self.fixture.state["cleaned"])

    def test_lost_network_response_recovered_before_removal(self):
        self.fixture.state["network_intent"] = "lp-shared-test"
        info = json.dumps(
            [{"Id": "network-id", "Labels": {"loomplane.acceptance": "lp-shared-test"}}]
        )

        def docker(*args, **kwargs):
            if args[0] == "info":
                return "parent"
            if args == ("network", "inspect", "lp-shared-test"):
                return info
            if args == ("network", "rm", "network-id"):
                self.assertEqual(
                    json.loads(self.path.read_text())["network"], "network-id"
                )
                return ""
            if args == ("network", "inspect", "network-id"):
                raise subprocess.CalledProcessError(
                    1, args, stderr=b"Error: No such network: network-id"
                )
            self.fail(args)

        with patch.object(self.fixture, "docker", side_effect=docker):
            self.fixture.cleanup()
        self.assertTrue(self.fixture.state["cleaned"])

    def test_cleanup_refuses_unowned_name_collision(self):
        self.fixture.state["containers"] = [
            {"role": "host", "name": "collision", "id": None}
        ]
        with (
            patch.object(
                self.fixture,
                "docker",
                side_effect=[
                    "parent",
                    json.dumps([{"Id": "unrelated", "Config": {"Labels": {}}}]),
                ],
            ),
            self.assertRaisesRegex(RuntimeError, "owner mismatch"),
        ):
            self.fixture.cleanup()


if __name__ == "__main__":
    unittest.main()
