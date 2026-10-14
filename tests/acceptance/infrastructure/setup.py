#!/usr/bin/env python3
"""Create or remove only the infrastructure recorded for this acceptance run."""

import argparse
import json
import os
import shutil
import socket
import subprocess
import uuid
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
KIND = (
    os.environ.get("KIND_BIN")
    or shutil.which("kind")
    or str(Path.home() / "go/bin/kind")
)
KUBECTL = os.environ.get("KUBECTL_BIN") or shutil.which("kubectl")


def command(args, input_text=None, env=None):
    result = subprocess.run(
        args, input=input_text, text=True, capture_output=True, check=True, env=env
    )
    return result.stdout.strip()


def port():
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        return listener.getsockname()[1]


def save(path, state):
    path.write_text(json.dumps(state, indent=2) + "\n")
    path.chmod(0o600)


def setup():
    run_id = "loomplane-e2e-" + uuid.uuid4().hex[:10]
    directory = (
        ROOT
        / ".superpowers/sdd/2026-10-13-own-infrastructure-live-deployments"
        / run_id
    )
    directory.mkdir(parents=True)
    path = directory / "state.json"
    state = {
        "run_id": run_id,
        "cluster": run_id,
        "namespace": run_id,
        "builder": run_id + "-builder",
        "network": run_id + "-network",
        "daemon_port": port(),
        "registry_port": port(),
        "private_registry_port": port(),
        "service_port": port(),
        "registry": "registry",
        "kubeconfig": str(directory / "kubeconfig"),
        "existing_containers": {},
        "created": [],
    }
    for identity in command(["docker", "ps", "-q"]).splitlines():
        state["existing_containers"][identity] = command(
            ["docker", "inspect", "--format", "{{.Name}}", identity]
        )
    save(path, state)
    try:
        # The real build daemon is disposable. Its cache and containers never
        # share the user's Docker daemon; only this outer fixture runs there.
        command(
            [
                "docker",
                "run",
                "-d",
                "--privileged",
                "--cpus",
                "2",
                "--memory",
                "2g",
                "--name",
                state["builder"],
                "--label",
                "loomplane.acceptance=" + run_id,
                "-e",
                "DOCKER_TLS_CERTDIR=",
                "-p",
                f"127.0.0.1:{state['daemon_port']}:2375",
                "-p",
                f"127.0.0.1:{state['registry_port']}:{state['registry_port']}",
                "-p",
                f"127.0.0.1:{state['service_port']}:{state['service_port']}",
                "-p",
                f"127.0.0.1:{state['private_registry_port']}:{state['private_registry_port']}",
                "docker:29-dind",
            ]
        )
        state["created"].append("builder")
        save(path, state)
        command(
            [
                "docker",
                "network",
                "create",
                "--label",
                "loomplane.acceptance=" + run_id,
                state["network"],
            ]
        )
        command(
            [
                KIND,
                "create",
                "cluster",
                "--name",
                run_id,
                "--kubeconfig",
                state["kubeconfig"],
                "--wait",
                "120s",
            ],
            env={**os.environ, "KIND_EXPERIMENTAL_DOCKER_NETWORK": state["network"]},
        )
        state["created"].append("cluster")
        save(path, state)
        command(["docker", "network", "connect", state["network"], state["builder"]])
        # Wait for the isolated daemon without changing host Docker settings.
        command(
            [
                "docker",
                "exec",
                state["builder"],
                "sh",
                "-c",
                "for i in $(seq 1 120); do docker info >/dev/null 2>&1 && exit 0; sleep 1; done; exit 1",
            ]
        )
        # Resolve the host once outside nested BuildKit's DNS namespace.
        # Docker Desktop exposes the Mac through this address.
        state["host_address"] = command(
            [
                "docker",
                "exec",
                state["builder"],
                "getent",
                "hosts",
                "host.docker.internal",
            ]
        ).split()[0]
        save(path, state)
        command(
            [
                "docker",
                "exec",
                state["builder"],
                "docker",
                "run",
                "-d",
                "--name",
                state["registry"],
                "-p",
                f"{state['registry_port']}:5000",
                "registry:3",
            ]
        )
        for node in command([KIND, "get", "nodes", "--name", run_id]).splitlines():
            for registry_port in (
                state["registry_port"],
                state["private_registry_port"],
            ):
                registry_dir = "/etc/containerd/certs.d/localhost:" + str(registry_port)
                command(["docker", "exec", node, "mkdir", "-p", registry_dir])
                command(
                    [
                        "docker",
                        "exec",
                        "-i",
                        node,
                        "cp",
                        "/dev/stdin",
                        registry_dir + "/hosts.toml",
                    ],
                    f'[host."http://{state["builder"]}:{registry_port}"]\n  capabilities = ["pull", "resolve"]\n',
                )
        command(
            [
                KUBECTL,
                "--kubeconfig",
                state["kubeconfig"],
                "--context",
                "kind-" + run_id,
                "create",
                "namespace",
                state["namespace"],
            ]
        )
        save(path, state)
        print(path)
    except BaseException:
        cleanup(path)
        raise


def cleanup(path):
    state = json.loads(path.read_text())
    run_id = state["run_id"]
    if (
        not run_id.startswith("loomplane-e2e-")
        or state["builder"] != run_id + "-builder"
        or state["cluster"] != run_id
    ):
        raise ValueError("refusing cleanup of an unrecognized ownership record")
    clusters = command([KIND, "get", "clusters"]).splitlines()
    if state["cluster"] in clusters:
        command(
            [
                KIND,
                "delete",
                "cluster",
                "--name",
                state["cluster"],
                "--kubeconfig",
                state["kubeconfig"],
            ]
        )
    containers = command(
        ["docker", "ps", "-a", "--filter", "name=^/" + state["builder"] + "$", "-q"]
    ).splitlines()
    for identity in containers:
        owner = command(
            [
                "docker",
                "inspect",
                "--format",
                '{{index .Config.Labels "loomplane.acceptance"}}',
                identity,
            ]
        )
        if owner != run_id:
            raise ValueError("builder ownership changed; refusing deletion")
        command(["docker", "rm", "-fv", identity])
    if "network" in state:
        networks = command(
            [
                "docker",
                "network",
                "ls",
                "--filter",
                "name=^" + state["network"] + "$",
                "-q",
            ]
        ).splitlines()
        for identity in networks:
            owner = command(
                [
                    "docker",
                    "network",
                    "inspect",
                    "--format",
                    '{{index .Labels "loomplane.acceptance"}}',
                    identity,
                ]
            )
            if owner != run_id:
                raise ValueError("network ownership changed; refusing deletion")
            command(["docker", "network", "rm", identity])
    for identity in state["existing_containers"]:
        command(["docker", "inspect", "--format", "{{.Id}}", identity])
    state["cleaned"] = True
    save(path, state)


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--cleanup", type=Path)
    args = parser.parse_args()
    if args.cleanup:
        cleanup(args.cleanup)
    else:
        setup()
