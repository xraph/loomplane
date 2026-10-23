import json
import os
import signal
import subprocess
import sys
import tempfile
import threading
import time
import unittest
from pathlib import Path
from unittest.mock import MagicMock, patch

from live import HERE, Live, process_identity, save, stop_exact


class LiveSafetyTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.live = Live(Path(directory.name) / "state.json")
        save(self.live.path, {"evidence": {}})
        save(self.live.directory / "callback.json", {"deadline": time.time() + 60})

    def test_unapproved_run_never_launches(self):
        save(self.live.scope_path, {"run_id": "test"})
        with (
            patch.object(self.live, "checked_scope", return_value={"run_id": "test"}),
            patch("live.subprocess.Popen") as launch,
        ):
            with self.assertRaisesRegex(RuntimeError, "exact prepared scope"):
                self.live.run("wrong")
            launch.assert_not_called()

    def test_rerun_never_launches_more_hosts(self):
        from live import digest

        save(self.live.scope_path, {"run_id": "test"})
        save(self.live.process_path, {})
        with (
            patch.object(self.live, "checked_scope", return_value={"run_id": "test"}),
            patch("live.subprocess.Popen") as launch,
        ):
            with self.assertRaisesRegex(RuntimeError, "already attempted"):
                self.live.run(digest(self.live.scope_path))
            launch.assert_not_called()

    def test_deadline_stops_run(self):
        child = MagicMock()
        child.poll.return_value = None
        with self.assertRaisesRegex(RuntimeError, "deadline reached"):
            self.live.supervise(child, time.time() - 1)

    def test_enrollment_completion_closes_tunnel(self):
        save(self.live.path, {"evidence": {"enrollment_complete": True}})
        child = MagicMock(returncode=0)
        child.poll.side_effect = [None, 0]
        with patch.object(self.live, "stop") as stop, patch("live.time.sleep"):
            self.live.supervise(child, time.time() + 10)
        self.assertEqual(
            [call.args[0] for call in stop.call_args_list], ["tunnel", "proxy"]
        )

    def test_pid_reuse_is_preserved(self):
        with (
            patch("live.process_identity", return_value="foreign process"),
            patch("live.os.killpg") as kill,
        ):
            with self.assertRaisesRegex(RuntimeError, "identity drift"):
                stop_exact({"pid": 12, "identity": "owned process"})
            kill.assert_not_called()

    def test_cleanup_drains_before_provider_delete(self):
        save(self.live.scope_path, {"run_id": "test"})
        events = []
        with (
            patch.object(self.live, "checked_scope"),
            patch.object(
                self.live, "stop", side_effect=lambda role: events.append(role)
            ),
            patch.object(
                self.live,
                "invoke",
                side_effect=lambda *_args, **_kwargs: events.append("provider"),
            ),
        ):
            self.live.cleanup()
        self.assertEqual(events, ["runner", "tunnel", "proxy", "provider"])

    def test_failed_shutdown_blocks_provider_delete(self):
        with (
            patch.object(self.live, "checked_scope"),
            patch.object(
                self.live, "stop", side_effect=RuntimeError("unknown process")
            ),
            patch.object(self.live, "invoke") as invoke,
        ):
            with self.assertRaises(RuntimeError):
                self.live.cleanup()
            invoke.assert_not_called()

    def test_unresolved_runner_still_stops_public_processes(self):
        save(self.live.process_path, {"runner": {"phase": "intent"}})
        calls = []
        original_stop = self.live.stop

        def record_stop(role):
            calls.append(role)
            original_stop(role)

        with (
            patch.object(self.live, "checked_scope"),
            patch.object(self.live, "stop", side_effect=record_stop),
            patch.object(self.live, "invoke") as provider,
        ):
            with self.assertRaisesRegex(RuntimeError, "launch unresolved"):
                self.live.cleanup()
            provider.assert_not_called()
        self.assertEqual(calls, ["runner", "tunnel", "proxy"])

    def test_partial_tunnel_start_runs_cleanup(self):
        from live import digest

        save(self.live.scope_path, {"run_id": "test"})
        proxy, tunnel = MagicMock(), MagicMock()
        tunnel.stdout = []
        tunnel.poll.return_value = 1
        with (
            patch.object(self.live, "checked_scope", return_value={"run_id": "test"}),
            patch.object(self.live, "launch", side_effect=[proxy, tunnel]),
            patch.object(self.live, "cleanup") as cleanup,
        ):
            with self.assertRaisesRegex(RuntimeError, "did not publish"):
                self.live.run(digest(self.live.scope_path))
            cleanup.assert_called_once()

    def test_cleanup_scope_does_not_need_callback_executables(self):
        from live import digest

        self.live.binary.write_bytes(b"pinned cleanup executable")
        save(self.live.path, {"run_id": "test", "account": "account"})
        scope = {
            "run_id": "test",
            "account": "account",
            "binary_sha256": digest(self.live.binary),
        }
        save(self.live.scope_path, scope)
        with patch("live.BINARY", self.live.directory / "missing-cloudflared"):
            self.assertEqual(self.live.checked_scope(), scope)

    def test_persist_private_and_atomic(self):
        save(self.live.path, {"intent": "one"})
        self.assertEqual(self.live.path.stat().st_mode & 0o777, 0o600)
        self.assertEqual(json.loads(self.live.path.read_text()), {"intent": "one"})
        self.assertFalse(self.live.path.with_suffix(".tmp").exists())


class RealLaunchGateTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.live = Live(Path(directory.name) / "state.json")
        save(self.live.scope_path, {"run_id": "local-gate-test"})
        self.effect = self.live.directory / "effect"
        self.command = [
            sys.executable,
            "-c",
            (
                "from pathlib import Path; import sys,time; "
                "Path(sys.argv[1]).write_text('effect'); time.sleep(60)"
            ),
            str(self.effect),
        ]

    def reap_owned(self, child):
        if child.poll() is None:
            os.killpg(child.pid, signal.SIGKILL)
        child.wait(timeout=5)

    def wait_until(self, predicate):
        until = time.monotonic() + 10
        while not predicate():
            if time.monotonic() >= until:
                self.fail("local subprocess did not reach the expected state")
            time.sleep(0.02)

    def test_released_roles_have_stable_receipts_and_fresh_cleanup_stops_them(self):
        for role in ("runner", "tunnel", "proxy"):
            with self.subTest(role=role):
                child = self.live.launch(self.command, role=role)
                self.addCleanup(self.reap_owned, child)
                self.wait_until(self.effect.exists)
                receipt = json.loads(self.live.process_path.read_text())[role]
                self.assertEqual(receipt["phase"], "ready")
                self.assertEqual(process_identity(child.pid), receipt["identity"])
                # Reap the child as an OS parent would after a supervisor exits.
                threading.Thread(target=child.wait, daemon=True).start()
                fresh = Live(self.live.path)
                with (
                    patch.object(fresh, "checked_scope"),
                    patch.object(fresh, "invoke") as provider,
                ):
                    fresh.cleanup()
                    provider.assert_called_once()
                self.assertIsNotNone(child.poll())
                self.effect.unlink()

    def test_gate_preserves_target_exit_status(self):
        child = self.live.launch(
            [sys.executable, "-c", "raise SystemExit(23)"], role="runner"
        )
        self.assertEqual(child.wait(timeout=5), 23)
        for role, signum in (("proxy", signal.SIGTERM), ("tunnel", signal.SIGKILL)):
            child = self.live.launch(
                [sys.executable, "-c", f"import os; os.kill(os.getpid(), {signum})"],
                role=role,
            )
            self.assertEqual(child.wait(timeout=5), -signum)

    def test_shutdown_waits_for_signal_resistant_target(self):
        command = [
            sys.executable,
            "-c",
            (
                "import signal,sys,time; from pathlib import Path; "
                "signal.signal(signal.SIGTERM, signal.SIG_IGN); "
                "Path(sys.argv[1]).write_text('effect'); time.sleep(60)"
            ),
            str(self.effect),
        ]
        child = self.live.launch(command, role="runner")
        self.addCleanup(self.reap_owned, child)
        self.wait_until(self.effect.exists)
        os.killpg(child.pid, signal.SIGTERM)
        time.sleep(0.1)
        self.assertIsNone(child.poll())
        before = time.monotonic()
        self.live.stop("runner")
        self.assertIsNotNone(child.poll())
        self.assertLess(time.monotonic() - before, 12)

    def interrupted_launch(self, role, interruption):
        marker = self.live.directory / "gate-created.json"
        parent_code = """
import json, signal, sys, time
from pathlib import Path
from live import Live, save
live = Live(sys.argv[1])
def pause_before_receipt(role, child):
    save(Path(sys.argv[2]), {"pid": child.pid})
    while True:
        time.sleep(0.02)
def interrupted(*_):
    raise KeyboardInterrupt
signal.signal(signal.SIGTERM, interrupted)
live.remember = pause_before_receipt
try:
    live.launch(json.loads(sys.argv[4]), role=sys.argv[3])
except KeyboardInterrupt:
    pass
finally:
    for child in live.children.values():
        child.wait(timeout=5)
"""
        parent = subprocess.Popen(
            [
                sys.executable,
                "-c",
                parent_code,
                str(self.live.path),
                str(marker),
                role,
                json.dumps(self.command),
            ],
            env={**os.environ, "PYTHONPATH": str(HERE)},
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )
        self.addCleanup(lambda: parent.poll() is None and parent.kill())
        self.wait_until(marker.exists)
        pid = json.loads(marker.read_text())["pid"]
        intent = json.loads(self.live.process_path.read_text())[role]
        self.assertEqual(intent["phase"], "intent")
        self.assertNotIn("pid", intent)
        time.sleep(0.15)
        self.assertFalse(self.effect.exists())
        parent.send_signal(interruption)
        parent.wait(timeout=10)

        def exited():
            result = subprocess.run(
                ["ps", "-p", str(pid), "-o", "stat="],
                capture_output=True,
                text=True,
                check=False,
            )
            return result.returncode == 1 or result.stdout.strip().startswith("Z")

        self.wait_until(exited)
        self.assertFalse(self.effect.exists())
        fresh = Live(self.live.path)
        with (
            patch.object(fresh, "checked_scope"),
            patch.object(fresh, "invoke") as provider,
        ):
            with self.assertRaisesRegex(RuntimeError, "launch unresolved"):
                fresh.cleanup()
            provider.assert_not_called()

    def test_ordinary_interruption_before_receipt_never_releases_child(self):
        for role in ("runner", "tunnel", "proxy"):
            with self.subTest(role=role):
                self.interrupted_launch(role, signal.SIGTERM)
                self.live.process_path.unlink()
                (self.live.directory / "gate-created.json").unlink()

    def test_abrupt_parent_loss_before_receipt_never_releases_child(self):
        for role in ("runner", "tunnel", "proxy"):
            with self.subTest(role=role):
                self.interrupted_launch(role, signal.SIGKILL)
                self.live.process_path.unlink()
                (self.live.directory / "gate-created.json").unlink()
