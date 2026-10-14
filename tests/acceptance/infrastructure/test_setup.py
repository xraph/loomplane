"""Keep fixture cleanup isolated from the operator's Kubernetes configuration."""

import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import setup


class CleanupTests(unittest.TestCase):
    def test_cluster_deletion_uses_the_recorded_kubeconfig(self):
        run = "loomplane-e2e-cleanup-test"
        with tempfile.TemporaryDirectory() as directory:
            state_path = Path(directory) / "state.json"
            kubeconfig = str(Path(directory) / "kubeconfig")
            state_path.write_text(
                json.dumps(
                    {
                        "run_id": run,
                        "cluster": run,
                        "builder": run + "-builder",
                        "kubeconfig": kubeconfig,
                        "existing_containers": {},
                    }
                )
            )
            calls = []

            def command(args, **kwargs):
                calls.append(args)
                return run if args == [setup.KIND, "get", "clusters"] else ""

            with patch.object(setup, "command", command):
                setup.cleanup(state_path)
            self.assertIn(
                [
                    setup.KIND,
                    "delete",
                    "cluster",
                    "--name",
                    run,
                    "--kubeconfig",
                    kubeconfig,
                ],
                calls,
            )
            self.assertTrue(json.loads(state_path.read_text())["cleaned"])


if __name__ == "__main__":
    unittest.main()
