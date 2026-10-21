#!/usr/bin/env python3
"""Qualify prepared agents on an owned kind cluster with SPIRE PSAT and CSI."""

import base64
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import time
import uuid

ROOT = Path(__file__).resolve().parents[4]
images = {
    "node": "kindest/node@sha256:a1ed56cfb0e7b93589bdf97c8cd566405a265939e3620fc4f5de89adff580ae5",
    "server": "ghcr.io/spiffe/spire-server:1.15.4@sha256:a728418a76e5054e977680a7c70a291f93389294e4bef00e2aaad65324f39357",
    "agent": "ghcr.io/spiffe/spire-agent:1.15.4@sha256:adc9e89e171af5764b29f9ffc9d3296b8033ba8c6ca11bcadbb56099170d526e",
    "csi": "ghcr.io/spiffe/spiffe-csi-driver:0.2.13@sha256:6f1ba5d635ba9bf55ae69ba11d71bf169c8fa3e0139a5063738cbc227c8b5546",
    "registrar": "registry.k8s.io/sig-storage/csi-node-driver-registrar:v2.15.0@sha256:11f199f6bec47403b03cb49c79a41f445884b213b382582a60710b8c6fdc316a",
    "postgres": "postgres:17.11-alpine@sha256:b0f9560a2de083e2cc7382e75f808c7381a32852a7ec49117deedb300e552b24",
}


def object_(kind, name, namespace="spire", **fields):
    api = {
        "DaemonSet": "apps/v1",
        "ClusterRole": "rbac.authorization.k8s.io/v1",
        "ClusterRoleBinding": "rbac.authorization.k8s.io/v1",
        "Role": "rbac.authorization.k8s.io/v1",
        "RoleBinding": "rbac.authorization.k8s.io/v1",
        "CSIDriver": "storage.k8s.io/v1",
    }.get(kind, "v1")
    metadata = {"name": name}
    if namespace:
        metadata["namespace"] = namespace
    return {"apiVersion": api, "kind": kind, "metadata": metadata, **fields}


def mount(name, path, **options):
    return {"name": name, "mountPath": path, **options}


def config_volume(name):
    return {"name": name, "configMap": {"name": name}}


def host_volume(name, path):
    return {"name": name, "hostPath": {"path": path, "type": "DirectoryOrCreate"}}


def daemon(name, spec):
    return object_(
        "DaemonSet",
        name,
        spec={
            "selector": {"matchLabels": {"app": name}},
            "template": {"metadata": {"labels": {"app": name}}, "spec": spec},
        },
    )


def manifests(image, kubelet_ca, image_indexes="[]"):
    server_config = """server {
 bind_address = "0.0.0.0"
 bind_port = "8081"
 socket_path = "/run/spire/admin/server.sock"
 trust_domain = "loomplane.test"
 data_dir = "/run/spire/data"
 log_level = "INFO"
}
plugins {
 DataStore "sql" { plugin_data { database_type = "sqlite3" connection_string = "/run/spire/data/store.db" } }
 KeyManager "memory" { plugin_data {} }
 NodeAttestor "k8s_psat" { plugin_data { clusters = { "qualification" = { service_account_allow_list = ["spire:spire-agent"] } } } }
 Notifier "k8sbundle" { plugin_data {} }
}
"""
    agent_config = """agent {
 data_dir = "/run/spire/data"
 server_address = "spire-server.spire.svc"
 server_port = "8081"
 socket_path = "/run/spire/sockets/agent.sock"
 trust_bundle_path = "/bundle/bundle.crt"
 trust_domain = "loomplane.test"
 log_level = "INFO"
}
plugins {
 NodeAttestor "k8s_psat" { plugin_data { cluster = "qualification" } }
 KeyManager "memory" { plugin_data {} }
 WorkloadAttestor "k8s" { plugin_data { kubelet_ca_path = "/config/kubelet.crt" } }
}
"""
    docs = [object_("Namespace", ns, None) for ns in ("spire", "apps")]
    docs += [
        object_("ServiceAccount", sa)
        for sa in ("spire-server", "spire-agent", "spiffe-csi")
    ]
    roles = {
        "spire-server": [
            {
                "apiGroups": [""],
                "resources": ["nodes", "namespaces", "pods"],
                "verbs": ["get"],
            },
            {
                "apiGroups": ["authentication.k8s.io"],
                "resources": ["tokenreviews"],
                "verbs": ["create"],
            },
        ],
        "spire-agent": [
            {
                "apiGroups": [""],
                "resources": ["nodes", "pods", "nodes/proxy"],
                "verbs": ["get"],
            }
        ],
    }
    for name, rules in roles.items():
        docs.append(object_("ClusterRole", name, None, rules=rules))
        docs.append(
            object_(
                "ClusterRoleBinding",
                name,
                None,
                subjects=[
                    {"kind": "ServiceAccount", "name": name, "namespace": "spire"}
                ],
                roleRef={
                    "apiGroup": "rbac.authorization.k8s.io",
                    "kind": "ClusterRole",
                    "name": name,
                },
            )
        )
    for ns, resources in (
        ("spire", ["configmaps"]),
        ("apps", ["pods", "serviceaccounts", "configmaps"]),
    ):
        docs.append(
            object_(
                "Role",
                "fixture",
                ns,
                rules=[
                    {
                        "apiGroups": [""],
                        "resources": resources,
                        "verbs": ["get", "list", "create", "delete", "patch"],
                    }
                ] + ([
                    {"apiGroups": ["apps"], "resources": ["deployments"], "verbs": ["get", "create", "update", "delete"]},
                    {"apiGroups": ["apps"], "resources": ["replicasets"], "verbs": ["get"]},
                    {"apiGroups": [""], "resources": ["secrets"], "verbs": ["get", "create", "update", "delete"]},
                    {"apiGroups": [""], "resources": ["services"], "verbs": ["get"]},
                ] if ns == "apps" else []),
            )
        )
        docs.append(
            object_(
                "RoleBinding",
                "fixture",
                ns,
                subjects=[
                    {
                        "kind": "ServiceAccount",
                        "name": "spire-server",
                        "namespace": "spire",
                    }
                ],
                roleRef={
                    "apiGroup": "rbac.authorization.k8s.io",
                    "kind": "Role",
                    "name": "fixture",
                },
            )
        )
    docs.append(
        object_(
            "Role",
            "probe-logs",
            "apps",
            rules=[{"apiGroups": [""], "resources": ["pods/log"], "verbs": ["get"]}],
        )
    )
    docs.append(
        object_(
            "RoleBinding",
            "probe-logs",
            "apps",
            subjects=[
                {"kind": "ServiceAccount", "name": "spire-server", "namespace": "spire"}
            ],
            roleRef={
                "apiGroup": "rbac.authorization.k8s.io",
                "kind": "Role",
                "name": "probe-logs",
            },
        )
    )
    docs += [
        object_("ConfigMap", "spire-server", data={"server.conf": server_config}),
        object_(
            "ConfigMap",
            "spire-agent",
            data={"agent.conf": agent_config, "kubelet.crt": kubelet_ca},
        ),
        object_("ConfigMap", "spire-bundle"),
    ]
    docs.append(
        daemon(
            "spire-agent",
            {
                "serviceAccountName": "spire-agent",
                "hostPID": True,
                "hostNetwork": True,
                "dnsPolicy": "ClusterFirstWithHostNet",
                "containers": [
                    {
                        "name": "spire-agent",
                        "image": images["agent"],
                        "args": ["-config", "/config/agent.conf"],
                        "env": [
                            {
                                "name": "MY_NODE_NAME",
                                "valueFrom": {
                                    "fieldRef": {"fieldPath": "spec.nodeName"}
                                },
                            }
                        ],
                        "volumeMounts": [
                            mount("spire-agent", "/config", readOnly=True),
                            mount("spire-bundle", "/bundle", readOnly=True),
                            mount("token", "/var/run/secrets/tokens", readOnly=True),
                            mount("socket", "/run/spire/sockets"),
                        ],
                    }
                ],
                "volumes": [
                    config_volume("spire-agent"),
                    config_volume("spire-bundle"),
                    {
                        "name": "token",
                        "projected": {
                            "sources": [
                                {
                                    "serviceAccountToken": {
                                        "path": "spire-agent",
                                        "audience": "spire-server",
                                        "expirationSeconds": 600,
                                    }
                                }
                            ]
                        },
                    },
                    host_volume("socket", "/run/spire/agent-sockets"),
                ],
            },
        )
    )
    docs.append(
        object_(
            "CSIDriver",
            "csi.spiffe.io",
            None,
            spec={
                "attachRequired": False,
                "podInfoOnMount": True,
                "fsGroupPolicy": "None",
                "volumeLifecycleModes": ["Ephemeral"],
            },
        )
    )
    docs.append(
        daemon(
            "spiffe-csi",
            {
                "serviceAccountName": "spiffe-csi",
                "automountServiceAccountToken": False,
                "containers": [
                    {
                        "name": "driver",
                        "image": images["csi"],
                        "args": [
                            "-workload-api-socket-dir",
                            "/spire-agent-socket",
                            "-csi-socket-path",
                            "/spiffe-csi/csi.sock",
                        ],
                        "env": [
                            {
                                "name": "MY_NODE_NAME",
                                "valueFrom": {
                                    "fieldRef": {"fieldPath": "spec.nodeName"}
                                },
                            }
                        ],
                        "securityContext": {
                            "privileged": True,
                            "readOnlyRootFilesystem": True,
                        },
                        "volumeMounts": [
                            mount("socket", "/spire-agent-socket", readOnly=True),
                            mount("csi", "/spiffe-csi"),
                            mount(
                                "pods",
                                "/var/lib/kubelet/pods",
                                mountPropagation="Bidirectional",
                            ),
                        ],
                    },
                    {
                        "name": "registrar",
                        "image": images["registrar"],
                        "args": [
                            "-csi-address",
                            "/spiffe-csi/csi.sock",
                            "-kubelet-registration-path",
                            "/var/lib/kubelet/plugins/csi.spiffe.io/csi.sock",
                        ],
                        "volumeMounts": [
                            mount("csi", "/spiffe-csi"),
                            mount("registration", "/registration"),
                        ],
                    },
                ],
                "volumes": [
                    host_volume("socket", "/run/spire/agent-sockets"),
                    host_volume("csi", "/var/lib/kubelet/plugins/csi.spiffe.io"),
                    host_volume("pods", "/var/lib/kubelet/pods"),
                    host_volume("registration", "/var/lib/kubelet/plugins_registry"),
                ],
            },
        )
    )
    for name, port in (("spire-server", 8081), ("control", 9443)):
        docs.append(
            object_(
                "Service",
                name,
                spec={
                    "selector": {"app": "qualification"},
                    "ports": [{"port": port, "targetPort": port}],
                },
            )
        )
    password_env = {
        "name": "POSTGRES_PASSWORD",
        "valueFrom": {"secretKeyRef": {"name": "database", "key": "password"}},
    }
    pod = object_(
        "Pod",
        "qualification",
        spec={
            "serviceAccountName": "spire-server",
            "restartPolicy": "Never",
            "containers": [
                {
                    "name": "spire-server",
                    "image": images["server"],
                    "args": ["-config", "/config/server.conf"],
                    "volumeMounts": [
                        mount("spire-server", "/config", readOnly=True),
                        mount("admin", "/run/spire/admin"),
                    ],
                },
                {
                    "name": "postgres",
                    "image": images["postgres"],
                    "env": [
                        password_env,
                        {"name": "POSTGRES_USER", "value": "fixture"},
                        {"name": "POSTGRES_DB", "value": "fixture"},
                    ],
                    "volumeMounts": [mount("database", "/var/lib/postgresql/data")],
                },
                {
                    "name": "fixture",
                    "image": image,
                    "imagePullPolicy": "IfNotPresent",
                    "command": [
                        "/qualification",
                        "-test.run=^TestPreparedKubernetesIdentity$",
                        "-test.v",
                        "-test.timeout=6m",
                    ],
                    "env": [
                        password_env,
                        {"name": "LOOMPLANE_KUBERNETES_QUALIFICATION", "value": "1"},
                        {"name": "AGENT_IMAGE", "value": image},
                        {"name": "APPLICATION_IMAGE_INDEXES", "value": image_indexes},
                        {
                            "name": "POD_NAME",
                            "valueFrom": {"fieldRef": {"fieldPath": "metadata.name"}},
                        },
                    ],
                    "volumeMounts": [
                        mount("admin", "/run/spire/admin"),
                        mount("workload", "/run/spire/workload", readOnly=True),
                        mount("tmp", "/tmp"),
                    ],
                },
            ],
            "volumes": [
                config_volume("spire-server"),
                {"name": "admin", "emptyDir": {}},
                {
                    "name": "database",
                    "emptyDir": {"medium": "Memory", "sizeLimit": "512Mi"},
                },
                {"name": "tmp", "emptyDir": {}},
                {
                    "name": "workload",
                    "csi": {"driver": "csi.spiffe.io", "readOnly": True},
                },
            ],
        },
    )
    pod["metadata"]["labels"] = {"app": "qualification"}
    docs.append(pod)
    return docs


def command(args, **options):
    return subprocess.run(
        args, check=True, text=True, timeout=options.pop("timeout", 180), **options
    )


def output(args):
    return command(args, stdout=subprocess.PIPE).stdout.strip()


def assert_qualified(logs, exit_code):
    if exit_code != 0 or not any(
        line.startswith("--- PASS: TestPreparedKubernetesIdentity (")
        for line in logs.splitlines()
    ):
        raise RuntimeError("Kubernetes identity qualification did not pass")


def run():
    kind = shutil.which("kind") or str(Path.home() / "go/bin/kind")
    architecture = output(["docker", "info", "--format", "{{.Architecture}}"])
    architecture = {"aarch64": "arm64", "x86_64": "amd64"}.get(
        architecture, architecture
    )
    if architecture not in ("arm64", "amd64"):
        raise SystemExit("Unsupported Docker architecture")
    name = "loomplane-psat-" + uuid.uuid4().hex[:10]
    tag = "loomplane-psat-fixture:" + name
    with tempfile.TemporaryDirectory(prefix="loomplane-psat-") as folder:
        path = Path(folder)
        kubeconfig = path / "kubeconfig"
        # Every Kubernetes operation names this run's kubeconfig. Ambient contexts
        # and pre-existing clusters are never used or changed.
        kubectl = ["kubectl", "--kubeconfig", str(kubeconfig)]
        created = False
        try:
            env = {
                **os.environ,
                "GOOS": "linux",
                "GOARCH": architecture,
                "CGO_ENABLED": "0",
                "GOWORK": "off",
            }
            command(
                [
                    "go",
                    "test",
                    "-c",
                    "-o",
                    str(path / "qualification"),
                    "./tests/acceptance/federation/kubernetes",
                ],
                cwd=ROOT,
                env=env,
            )
            command(
                [
                    "go",
                    "build",
                    "-o",
                    str(path / "loomplane-agent"),
                    "./cmd/loomplane-agent",
                ],
                cwd=ROOT,
                env=env,
            )
            (path / "Dockerfile").write_text(
                "FROM scratch\nCOPY qualification /qualification\nCOPY loomplane-agent /usr/local/bin/loomplane-agent\n"
            )
            command(["docker", "build", "--quiet", "-t", tag, str(path)])
            # Set before creation so a partial kind failure still gets cleaned up.
            created = True
            command(
                [
                    kind,
                    "create",
                    "cluster",
                    "--name",
                    name,
                    "--image",
                    images["node"],
                    "--kubeconfig",
                    str(kubeconfig),
                    "--wait",
                    "90s",
                ],
                timeout=240,
            )
            kubeconfig.chmod(0o600)
            command([kind, "load", "docker-image", "--name", name, tag])
            node = name + "-control-plane"
            listing = output(
                ["docker", "exec", node, "ctr", "-n", "k8s.io", "images", "ls"]
            )
            imported = next(
                row.split()
                for row in listing.splitlines()
                if row.startswith("docker.io/library/" + tag + " ")
            )
            image = "localhost/loomplane-psat@" + imported[2]
            command(
                [
                    "docker",
                    "exec",
                    node,
                    "ctr",
                    "-n",
                    "k8s.io",
                    "images",
                    "tag",
                    imported[0],
                    image,
                ]
            )
            ca = output(
                ["docker", "exec", node, "cat", "/var/lib/kubelet/pki/kubelet.crt"]
            )
            runtime_image = json.loads(output(["docker", "exec", node, "crictl", "inspecti", imported[0]]))["status"]
            digests = {imported[2]}
            digests.update(ref.rsplit("@", 1)[1] for ref in runtime_image["repoDigests"])
            if len(digests) > 4:
                raise RuntimeError("Too many runtime image proofs")
            image_indexes = []
            for digest in sorted(digests):
                raw = subprocess.check_output(
                    ["docker", "exec", node, "ctr", "-n", "k8s.io", "content", "get", digest], timeout=30
                )
                if not raw or len(raw) > 256 * 1024:
                    raise RuntimeError("Invalid runtime image proof size")
                image_indexes.append(base64.b64encode(raw).decode("ascii"))
            docs = manifests(image, ca, json.dumps(image_indexes))
            docs.insert(
                2,
                object_(
                    "Secret", "database", stringData={"password": uuid.uuid4().hex}
                ),
            )
            command(
                [*kubectl, "apply", "-f", "-"],
                input=json.dumps({"apiVersion": "v1", "kind": "List", "items": docs}),
            )
            deadline = time.monotonic() + 420
            while time.monotonic() < deadline:
                pod = json.loads(
                    output(
                        [
                            *kubectl,
                            "get",
                            "pod",
                            "qualification",
                            "-n",
                            "spire",
                            "-o",
                            "json",
                        ]
                    )
                )
                states = pod.get("status", {}).get("containerStatuses", [])
                fixture = next((c for c in states if c["name"] == "fixture"), {})
                terminal = fixture.get("state", {}).get("terminated")
                if terminal:
                    logs = output(
                        [
                            *kubectl,
                            "logs",
                            "qualification",
                            "-n",
                            "spire",
                            "-c",
                            "fixture",
                        ]
                    )
                    print(logs, flush=True)
                    assert_qualified(logs, terminal["exitCode"])
                    print("PASS local Kubernetes PSAT/CSI qualification", flush=True)
                    break
                time.sleep(2)
            else:
                raise TimeoutError(
                    "Kubernetes identity qualification exceeded seven minutes"
                )
        except (Exception, KeyboardInterrupt):
            if kubeconfig.exists():
                for args in (
                    ["get", "pods", "-A", "-o", "wide"],
                    ["logs", "qualification", "-n", "spire", "-c", "fixture"],
                    ["logs", "qualification", "-n", "spire", "-c", "spire-server"],
                    ["logs", "-n", "spire", "-l", "app=spire-agent", "--tail=50"],
                    ["get", "events", "-A", "--field-selector", "type=Warning"],
                ):
                    subprocess.run([*kubectl, *args], check=False, timeout=20)
            raise
        finally:
            try:
                if created:
                    command(
                        [
                            kind,
                            "delete",
                            "cluster",
                            "--name",
                            name,
                            "--kubeconfig",
                            str(kubeconfig),
                        ],
                        timeout=120,
                    )
            finally:
                # Always try the exact owned image, even if cluster deletion fails.
                subprocess.run(
                    ["docker", "image", "rm", tag],
                    check=False,
                    stdout=subprocess.DEVNULL,
                    stderr=subprocess.DEVNULL,
                    timeout=30,
                )


if __name__ == "__main__":
    run()
