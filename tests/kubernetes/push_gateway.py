#!/usr/bin/env python3
"""Exercise the installed push chart with HTTPS and disposable provider keys."""

import base64
import json
import os
import re
import secrets
import subprocess
import sys
import tempfile
import time
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
FIXTURES = Path(__file__).with_name("push-gateway")


def run(*args, data=None, timeout=180):
    result = subprocess.run(
        [str(arg) for arg in args],
        input=data,
        capture_output=True,
        timeout=timeout,
        check=False,
    )
    if result.returncode:
        raise RuntimeError(
            f"{args[0]} failed: {result.stderr.decode()} {result.stdout.decode()}"
        )
    return result.stdout


def retry(check, timeout=120):
    deadline = time.monotonic() + timeout
    error = None
    while time.monotonic() < deadline:
        try:
            return check()
        except (AssertionError, RuntimeError) as exc:
            error = exc
            time.sleep(2)
    raise AssertionError(f"Check did not pass within {timeout}s: {error}")


class Smoke:
    def __init__(self, directory, app_image, gateway_image):
        self.directory = Path(directory)
        self.cluster = f"loomplane-push-{os.getpid()}"
        self.kubeconfig = self.directory / "kubeconfig"
        self.app_image = app_image
        self.gateway_image = gateway_image
        self.port_forward = None
        self.created = False
        self.values_file = self.directory / "values.json"
        self.cookie_file = self.directory / "cookies"
        self.device_id = None

    def k(self, *args, **kwargs):
        return run(
            "kubectl",
            "--kubeconfig",
            self.kubeconfig,
            "-n",
            "loomplane",
            *args,
            **kwargs,
        )

    def secret(self):
        files = ["sealing-key", "fcm-service-account.json", "apns-key.p8"]
        if (self.directory / "previous-sealing-key").exists():
            files.append("previous-sealing-key")
        manifest = self.k(
            "create",
            "secret",
            "generic",
            "loomplane-push",
            "--dry-run=client",
            "-o",
            "json",
            *(f"--from-file={name}={self.directory / name}" for name in files),
        )
        self.k("apply", "-f", "-", data=manifest)

    def install(self):
        self.values_file.write_text(json.dumps(self.values))
        run(
            "helm",
            "--kubeconfig",
            self.kubeconfig,
            "upgrade",
            "--install",
            "loomplane",
            ROOT / "deployments/helm/loomplane",
            "-n",
            "loomplane",
            "-f",
            self.values_file,
            "--wait",
            "--timeout",
            "8m",
            timeout=540,
        )

    def request(self, host, path, body=None, headers=None, method=None):
        arguments = [
            "curl",
            "--silent",
            "--show-error",
            "--noproxy",
            "*",
            "--max-time",
            "20",
            "--cacert",
            str(self.directory / "tls.crt"),
            "--resolve",
            f"{host}:{self.port}:127.0.0.1",
            "--cookie",
            str(self.cookie_file),
            "--cookie-jar",
            str(self.cookie_file),
            "--output",
            str(self.directory / "response"),
            "--write-out",
            "%{http_code}",
        ]
        if body is not None:
            arguments += [
                "--header",
                "Content-Type: application/json",
                "--data-binary",
                json.dumps(body),
            ]
        if host == "app.loomplane.test":
            arguments += ["--header", "X-Publishable-Key: pk_test_loomplane_dev"]
        if method:
            arguments += ["--request", method]
        for key, value in (headers or {}).items():
            arguments += ["--header", f"{key}: {value}"]
        status = int(run(*arguments, f"https://{host}:{self.port}{path}"))
        response = (self.directory / "response").read_text()
        return status, response

    def expect(self, status, host, path, **kwargs):
        actual, response = self.request(host, path, **kwargs)
        assert actual == status, (host, path, actual, response[:500])
        return response

    def register(self, platform, server_key, key_id):
        body = {"platform": platform, "server_key": server_key, "token": "smoke-token"}
        if platform == "ios":
            body.update(token="abcdef0123456789", apns_env="production")

        def registered():
            response = self.expect(
                201, "push.loomplane.test", "/v1/registrations", body=body
            )
            handle = json.loads(response)["handle"]
            assert handle.startswith(f"h1.{key_id}."), handle
            return handle

        # A Deployment can become ready before its new endpoints reach the
        # ingress controller. Poll for the expected key through public TLS.
        return retry(registered)

    def signed_send(self, handle, expected):
        body = {
            "handle": handle,
            "key_id": "0123456789abcdef",
            "urgency": "active",
            "kind": "show",
            "collapse_id": "a1b2c3d4e5f60718",
            "ttl_seconds": 3600,
            "sealed": "c2VhbGVkIGJ5dGVz",
        }
        timestamp = str(int(time.time()))
        (self.directory / "signature-input").write_text(
            timestamp + "." + json.dumps(body)
        )
        signature = run(
            "openssl",
            "pkeyutl",
            "-sign",
            "-rawin",
            "-inkey",
            self.directory / "sender.pem",
            "-in",
            self.directory / "signature-input",
        )
        retry(
            lambda: self.expect(
                expected,
                "push.loomplane.test",
                "/v1/send",
                body=body,
                headers={
                    "X-Herald-Key": self.sender_key,
                    "X-Herald-Timestamp": timestamp,
                    "X-Herald-Signature": base64.b64encode(signature).decode(),
                },
            )
        )

    def wait_for_gateway(self):
        architecture = self.k(
            "get", "nodes", "-o", "jsonpath={.items[0].status.nodeInfo.architecture}"
        ).decode()
        assert architecture in ("amd64", "arm64"), architecture
        executable = self.directory / "push-network-probe"
        run(
            "env",
            "CGO_ENABLED=0",
            "GOOS=linux",
            f"GOARCH={architecture}",
            "go",
            "build",
            "-p",
            "2",
            "-o",
            executable,
            FIXTURES / "network-probe",
        )
        self.k(
            "exec",
            "-i",
            "deploy/loomplane",
            "-c",
            "api",
            "--",
            "sh",
            "-ec",
            "cat > /tmp/loomplane-push-probe; chmod 700 /tmp/loomplane-push-probe",
            data=executable.read_bytes(),
        )
        # Exercise the API container's DNS, TLS roots and gateway route before
        # the one real test push. Missing auth rejects this request before
        # handle parsing, rate limits or provider delivery.
        retry(
            lambda: self.k(
                "exec",
                "deploy/loomplane",
                "-c",
                "api",
                "--",
                "/tmp/loomplane-push-probe",
                timeout=20,
            )
        )

    def routes(self):
        retry(lambda: self.expect(200, "app.loomplane.test", "/"))
        for path in ["/", "/_/health", "/_/metrics", "/debug/", "/v1/send/extra"]:
            self.expect(404, "push.loomplane.test", path)
        self.expect(400, "push.loomplane.test", "/v1/registrations", body={})
        self.expect(401, "push.loomplane.test", "/v1/send", body={})
        # Traefik reports the deliberately empty backend differently for
        # Ingress and HTTPRoute. Both must refuse the public health request.
        blocked = 500 if self.values.get("httpRoute", {}).get("enabled") else 503
        self.expect(blocked, "app.loomplane.test", "/_/health")
        self.register(
            "android", self.sender_key, self.values["push"]["gateway"]["sealingKeyId"]
        )

    def setup(self):
        print("Creating disposable Kubernetes cluster", flush=True)
        clusters = run("kind", "get", "clusters").decode().splitlines()
        assert self.cluster not in clusters, "Refusing to reuse an existing cluster"
        self.created = True
        run(
            "kind",
            "create",
            "cluster",
            "--name",
            self.cluster,
            "--kubeconfig",
            self.kubeconfig,
            "--wait",
            "180s",
            timeout=240,
        )
        for image in [self.app_image, self.gateway_image]:
            print(f"Loading {image} into the test cluster", flush=True)
            run(
                "kind",
                "load",
                "docker-image",
                image,
                "--name",
                self.cluster,
                timeout=240,
            )
        # Public fixtures pull their native platform inside kind. Importing
        # Docker's partial multi-platform indexes can fail on containerd.
        self.k("create", "namespace", "loomplane")
        self.k(
            "apply",
            "--server-side",
            "-f",
            "https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.6.3/standard-install.yaml",
        )

        print("Installing TLS proxy and local provider fixture", flush=True)
        run(
            "openssl",
            "req",
            "-x509",
            "-newkey",
            "rsa:2048",
            "-nodes",
            "-days",
            "1",
            "-keyout",
            self.directory / "tls.key",
            "-out",
            self.directory / "tls.crt",
            "-subj",
            "/CN=push.loomplane.test",
            "-addext",
            "subjectAltName=DNS:push.loomplane.test,DNS:app.loomplane.test",
        )
        self.k(
            "create",
            "secret",
            "tls",
            "push-tls",
            f"--cert={self.directory / 'tls.crt'}",
            f"--key={self.directory / 'tls.key'}",
        )
        self.k("apply", "-f", FIXTURES / "proxy.yaml")
        self.k("rollout", "status", "deploy/traefik", "--timeout=180s")
        self.k(
            "run",
            "postgres",
            "--image=postgres:17-alpine",
            "--env=POSTGRES_USER=loomplane",
            "--env=POSTGRES_PASSWORD=test-only",
            "--env=POSTGRES_DB=loomplane",
        )
        self.k("expose", "pod", "postgres", "--port=5432")
        self.k("wait", "--for=condition=Ready", "pod/postgres", "--timeout=180s")
        retry(
            lambda: self.k(
                "exec",
                "postgres",
                "--",
                "pg_isready",
                "-h",
                "127.0.0.1",
                "-U",
                "loomplane",
            )
        )
        self.k(
            "create",
            "configmap",
            "fake-fcm",
            f"--from-file=provider.py={FIXTURES / 'provider.py'}",
        )
        self.k(
            "run",
            "fake-fcm",
            "--image=python:3.13-alpine",
            "--overrides",
            json.dumps(
                {
                    "spec": {
                        "containers": [
                            {
                                "name": "fake-fcm",
                                "image": "python:3.13-alpine",
                                "command": ["python", "/fixture/provider.py"],
                                "volumeMounts": [
                                    {"name": "fixture", "mountPath": "/fixture"}
                                ],
                            }
                        ],
                        "volumes": [
                            {"name": "fixture", "configMap": {"name": "fake-fcm"}}
                        ],
                    }
                }
            ),
        )
        self.k("expose", "pod", "fake-fcm", "--port=8080")
        self.k("wait", "--for=condition=Ready", "pod/fake-fcm", "--timeout=180s")
        proxy_ip = self.k(
            "get", "service", "traefik", "-o", "jsonpath={.spec.clusterIP}"
        ).decode()
        dns = json.loads(
            self.k("get", "configmap", "coredns", "-n", "kube-system", "-o", "json")
        )
        dns["data"]["Corefile"] = dns["data"]["Corefile"].replace(
            "ready",
            f"ready\n    hosts {{\n        {proxy_ip} push.loomplane.test app.loomplane.test\n        fallthrough\n    }}",
            1,
        )
        self.k("apply", "-n", "kube-system", "-f", "-", data=json.dumps(dns).encode())
        self.k("rollout", "restart", "deployment/coredns", "-n", "kube-system")
        self.k(
            "rollout",
            "status",
            "deployment/coredns",
            "-n",
            "kube-system",
            "--timeout=120s",
        )

        run(
            "openssl",
            "genpkey",
            "-algorithm",
            "EC",
            "-pkeyopt",
            "ec_paramgen_curve:P-256",
            "-out",
            self.directory / "apns-key.p8",
        )
        private = run(
            "openssl",
            "genpkey",
            "-algorithm",
            "RSA",
            "-pkeyopt",
            "rsa_keygen_bits:2048",
        ).decode()
        (self.directory / "fcm-service-account.json").write_text(
            json.dumps(
                {
                    "type": "service_account",
                    "project_id": "smoke",
                    "private_key_id": "smoke",
                    "private_key": private,
                    "client_email": "smoke@smoke.iam.gserviceaccount.com",
                    "token_uri": "http://fake-fcm:8080/token",
                }
            )
        )
        (self.directory / "sealing-key").write_text(
            base64.b64encode(secrets.token_bytes(32)).decode()
        )
        self.secret()
        run(
            "openssl",
            "genpkey",
            "-algorithm",
            "ED25519",
            "-out",
            self.directory / "sender.pem",
        )
        public = run(
            "openssl",
            "pkey",
            "-in",
            self.directory / "sender.pem",
            "-pubout",
            "-outform",
            "DER",
        )
        self.sender_key = base64.b64encode(public[-32:]).decode()
        self.values = {
            "publicURL": "https://app.loomplane.test",
            "image": {
                "repository": self.app_image.rsplit(":", 1)[0],
                "tag": self.app_image.rsplit(":", 1)[1],
                "pullPolicy": "Never",
            },
            "secrets": {
                "secretKey": base64.b64encode(secrets.token_bytes(32)).decode(),
                "databaseURL": "postgres://loomplane:test-only@postgres:5432/loomplane?sslmode=disable",
            },
            "storage": {"url": "file:///var/lib/loomplane"},
            "api": {"replicaCount": 1},
            "worker": {"replicaCount": 1},
            "extraEnv": [{"name": "SSL_CERT_FILE", "value": "/test-ca/tls.crt"}],
            "extraVolumes": [
                {
                    "name": "test-ca",
                    "secret": {
                        "secretName": "push-tls",
                        "items": [{"key": "tls.crt", "path": "tls.crt"}],
                    },
                },
                {"name": "artifacts", "emptyDir": {}},
            ],
            "extraVolumeMounts": [
                {"name": "test-ca", "mountPath": "/test-ca", "readOnly": True},
                {"name": "artifacts", "mountPath": "/var/lib/loomplane"},
            ],
            "ingress": {
                "enabled": True,
                "className": "traefik",
                "tls": [{"secretName": "push-tls", "hosts": ["app.loomplane.test"]}],
            },
            "push": {
                "gatewayUrl": "https://push.loomplane.test",
                "gateway": {
                    "enabled": True,
                    "image": {
                        "repository": self.gateway_image.rsplit(":", 1)[0],
                        "tag": self.gateway_image.rsplit(":", 1)[1],
                        "pullPolicy": "Never",
                    },
                    "existingSecret": "loomplane-push",
                    "sealingKeyId": "smoke1",
                    "apns": {
                        "enabled": True,
                        "keyId": "ABCDE12345",
                        "teamId": "TEAM123456",
                    },
                    "ingress": {
                        "enabled": True,
                        "className": "traefik",
                        "tls": [
                            {"secretName": "push-tls", "hosts": ["push.loomplane.test"]}
                        ],
                    },
                },
            },
        }
        self.install()
        run(
            "helm",
            "--kubeconfig",
            self.kubeconfig,
            "test",
            "loomplane",
            "-n",
            "loomplane",
            "--timeout",
            "2m",
        )
        with open(self.directory / "forward.log", "w") as forward_log:
            self.port_forward = subprocess.Popen(
                [
                    "kubectl",
                    "--kubeconfig",
                    str(self.kubeconfig),
                    "-n",
                    "loomplane",
                    "port-forward",
                    "svc/traefik",
                    ":443",
                ],
                stdout=forward_log,
                stderr=subprocess.STDOUT,
            )

        def port():
            match = re.search(
                r"127\.0\.0\.1:(\d+)", (self.directory / "forward.log").read_text()
            )
            assert match, "Waiting for the local HTTPS tunnel"
            return int(match.group(1))

        self.port = retry(port)

    def exercise(self):
        print(
            "Checking Ingress HTTPS, hidden health routes and mounted keys", flush=True
        )
        self.routes()
        for deployment in ["loomplane", "loomplane-worker"]:
            self.k(
                "exec",
                f"deploy/{deployment}",
                "--",
                "sh",
                "-ec",
                'test "$LOOMPLANE_PUSH_GATEWAY_URL" = https://push.loomplane.test; test ! -e /run/secrets/sealing-key',
            )
        self.k(
            "exec",
            "deploy/loomplane-push-gateway",
            "--",
            "sh",
            "-ec",
            'test "$(id -u)" = 10001; test -r /run/secrets/sealing-key; test -r /run/secrets/apns-key.p8; test -z "${LOOMPLANE_DATABASE_URL:-}"; test ! -e /var/run/secrets/kubernetes.io/serviceaccount/token',
        )
        print(
            "Sending an encrypted push from the installed Loomplane API to the local provider",
            flush=True,
        )
        self.k(
            "set",
            "env",
            "deploy/loomplane-push-gateway",
            "PUSH_GATEWAY_FCM_BASE_URL=http://fake-fcm:8080",
        )
        self.k("rollout", "status", "deploy/loomplane-push-gateway", "--timeout=120s")
        self.wait_for_gateway()
        token = (
            self.k(
                "exec", "deploy/loomplane", "--", "loomplane", "setup-token", "--raw"
            )
            .decode()
            .strip()
        )
        self.expect(
            200,
            "app.loomplane.test",
            "/api/v1/setup",
            body={
                "name": "Push Smoke",
                "email": "push@loomplane.test",
                "password": "Correct-Horse-Battery-9-Staple",
                "organization_name": "Push Smoke",
                "token": token,
            },
        )
        config = json.loads(
            self.expect(200, "app.loomplane.test", "/api/v1/push/config")
        )
        assert (
            config["enabled"] and config["gateway_url"] == "https://push.loomplane.test"
        )
        handle = self.register("android", config["server_key"], "smoke1")
        device = json.loads(
            self.expect(
                201,
                "app.loomplane.test",
                "/api/v1/push/devices",
                body={
                    "platform": "android",
                    "handle": handle,
                    "name": "Smoke Phone",
                    "time_zone": "UTC",
                    "public_key": base64.b64encode(secrets.token_bytes(32)).decode(),
                },
            )
        )
        self.device_id = device["id"]
        self.expect(
            200,
            "app.loomplane.test",
            f"/api/v1/push/devices/{device['id']}",
            body={"quiet_hours": None},
            method="PUT",
        )
        sent = self.expect(
            202,
            "app.loomplane.test",
            f"/api/v1/push/devices/{device['id']}/test",
            body={},
        )
        assert json.loads(sent)["status"] == "sent", sent
        assert b"accepted encrypted push" in self.k("logs", "fake-fcm")

        old_handle = self.register("ios", self.sender_key, "smoke1")
        self.k(
            "delete",
            "pod",
            "-l",
            "app.kubernetes.io/component=push-gateway",
            "--wait=true",
        )
        self.k("rollout", "status", "deploy/loomplane-push-gateway", "--timeout=120s")
        self.register("android", self.sender_key, "smoke1")
        print(
            "Checking key rotation, previous-key acceptance and retirement", flush=True
        )
        (self.directory / "previous-sealing-key").write_text(
            (self.directory / "sealing-key").read_text()
        )
        (self.directory / "sealing-key").write_text(
            base64.b64encode(secrets.token_bytes(32)).decode()
        )
        self.secret()
        gateway = self.values["push"]["gateway"]
        gateway.update(sealingKeyId="smoke2", previousSealingKeyId="smoke1")
        # With APNs off, sends return 502 locally after opening the old handle.
        # That verifies the previous key without contacting a real provider.
        gateway["apns"]["enabled"] = False
        self.install()
        self.register("android", self.sender_key, "smoke2")
        self.signed_send(old_handle, 502)
        gateway["previousSealingKeyId"] = ""
        self.install()
        self.signed_send(old_handle, 410)

        print("Switching the same release to HTTPS Gateway API routes", flush=True)
        gateway["ingress"]["enabled"] = False
        gateway["httpRoute"] = {"enabled": True, "parentRefs": [{"name": "push-test"}]}
        self.values["ingress"]["enabled"] = False
        self.values["httpRoute"] = {
            "enabled": True,
            "parentRefs": [{"name": "push-test"}],
        }
        self.install()
        retry(self.routes)
        parents = json.loads(
            self.k("get", "httproute", "loomplane-push-gateway", "-o", "json")
        )["status"]["parents"]
        assert any(
            any(
                c["type"] == "Accepted" and c["status"] == "True"
                for c in p["conditions"]
            )
            for p in parents
        )
        print("Disabling the bundled gateway in the same Helm release", flush=True)
        gateway["enabled"] = False
        self.values["push"]["gatewayUrl"] = "https://push.loomplane.com"
        self.install()
        assert not self.k(
            "get", "deploy", "loomplane-push-gateway", "--ignore-not-found"
        ).strip()
        assert not self.k(
            "get", "service", "loomplane-push-gateway", "--ignore-not-found"
        ).strip()
        assert not self.k(
            "get", "httproute", "loomplane-push-gateway", "--ignore-not-found"
        ).strip()
        retry(
            lambda: self.expect(
                404, "push.loomplane.test", "/v1/registrations", body={}
            )
        )

        def remote_config():
            response = self.expect(200, "app.loomplane.test", "/api/v1/push/config")
            config = json.loads(response)
            assert config["gateway_url"] == "https://push.loomplane.com", config

        # Old API pods continue serving while their shutdown delay drains
        # existing connections. Wait until the new configuration is public.
        retry(remote_config)
        print(
            "PASS: Helm push gateway, API delivery, TLS, Ingress, HTTPRoute, restart, rotation and disable",
            flush=True,
        )

    def close(self):
        if self.port_forward:
            self.port_forward.terminate()
            self.port_forward.wait(timeout=15)
        if self.created:
            run(
                "kind",
                "delete",
                "cluster",
                "--name",
                self.cluster,
                "--kubeconfig",
                self.kubeconfig,
            )


def main():
    if len(sys.argv) != 3:
        sys.exit(
            "Usage: python3 tests/kubernetes/push_gateway.py APP_IMAGE GATEWAY_IMAGE"
        )
    with tempfile.TemporaryDirectory(prefix="loomplane-push-kind-") as directory:
        smoke = Smoke(directory, *sys.argv[1:])
        try:
            smoke.setup()
            smoke.exercise()
        except Exception:
            if smoke.created:
                for args in [
                    ("get", "pods", "-o", "wide"),
                    ("get", "events", "--sort-by=.lastTimestamp"),
                    ("get", "endpointslices", "-o", "wide"),
                    ("logs", "deploy/traefik", "--tail=30"),
                    ("logs", "deploy/loomplane-push-gateway", "--tail=30"),
                    ("logs", "deploy/loomplane", "--tail=30"),
                    ("logs", "fake-fcm", "--tail=30"),
                ]:
                    try:
                        print(smoke.k(*args).decode(), file=sys.stderr)
                    except RuntimeError:
                        pass
                if smoke.device_id:
                    try:
                        print(
                            smoke.k(
                                "exec",
                                "-i",
                                "postgres",
                                "--",
                                "psql",
                                "-X",
                                "-U",
                                "loomplane",
                                "-d",
                                "loomplane",
                                "-v",
                                "ON_ERROR_STOP=1",
                                "-v",
                                f"device_id={smoke.device_id}",
                                data=(
                                    "SELECT recipient AS device_id, channel, status, error "
                                    "FROM herald_messages "
                                    "WHERE channel = 'push' AND recipient = :'device_id' "
                                    "ORDER BY created_at DESC LIMIT 5;\n"
                                ).encode(),
                            ).decode(),
                            file=sys.stderr,
                        )
                    except RuntimeError:
                        pass
            raise
        finally:
            smoke.close()


if __name__ == "__main__":
    main()
