import json
import tempfile
import time
import unittest
from pathlib import Path
from unittest.mock import MagicMock, patch

from live import Live, save, stop_exact


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

    def test_partial_tunnel_start_runs_cleanup(self):
        from live import digest

        save(self.live.scope_path, {"run_id": "test"})
        proxy, tunnel = MagicMock(), MagicMock()
        tunnel.stdout = []
        tunnel.poll.return_value = 1
        with (
            patch.object(self.live, "checked_scope", return_value={"run_id": "test"}),
            patch("live.subprocess.Popen", side_effect=[proxy, tunnel]),
            patch.object(self.live, "remember"),
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
