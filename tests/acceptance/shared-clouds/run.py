#!/usr/bin/env python3
"""Run shared-cloud fixtures inside an isolated Docker network."""

import argparse
import json
import os
import re
import subprocess
import time
import uuid
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
HERE = Path(__file__).resolve().parent
PEBBLE = "ghcr.io/letsencrypt/pebble:latest@sha256:ddf230642b1a584f519f32e347de1b05a6e4c1f6c35c1863b33effeab5f78199"
WHOAMI = "traefik/whoami:v1.11.0@sha256:200689790a0a0ea48ca45992e0450bc26ccab5307375b41c84dfc4f2475937ab"
CADDY = "caddy:2.11.7-alpine@sha256:d8542f48d34a9cf4e4c11a478865229840e87e4c96ea3f439101f31a5d35f75f"


def command(args, data=None, timeout=120):
    result = subprocess.run(
        args, input=data, capture_output=True, timeout=timeout, check=False
    )
    if result.returncode:
        raise subprocess.CalledProcessError(
            result.returncode, args, output=result.stdout, stderr=result.stderr
        )
    return result.stdout.decode().strip()


def go_architecture(value):
    architectures = {
        "aarch64": "arm64",
        "arm64": "arm64",
        "x86_64": "amd64",
        "amd64": "amd64",
    }
    if value not in architectures:
        raise ValueError("unsupported Docker architecture: " + value)
    return architectures[value]


def save(path, state):
    temporary = path.with_suffix(".tmp")
    temporary.write_text(json.dumps(state, indent=2) + "\n")
    temporary.chmod(0o600)
    temporary.replace(path)


class Fixture:
    def __init__(self, path):
        self.path = Path(path).resolve()
        self.directory = self.path.parent
        self.state = json.loads(self.path.read_text()) if self.path.exists() else {}

    def persist(self):
        save(self.path, self.state)

    def docker(self, *args, **kwargs):
        return command(["docker", *args], **kwargs)

    def create(self, role, image, args=(), run=()):
        name = self.state["run_id"] + "-" + role
        # Intent precedes create; immutable ID precedes start and all later effects.
        resource = {"role": role, "name": name, "id": None}
        self.state["containers"].append(resource)
        self.persist()
        resource["id"] = self.docker(
            "create",
            "--name",
            name,
            "--label",
            "loomplane.acceptance=" + self.state["run_id"],
            *args,
            image,
            *run,
        )
        self.persist()
        self.docker("start", resource["id"])
        return resource["id"]

    def create_volume(self, name):
        if self.inspect("volume", name) is not None:
            raise RuntimeError("fixture volume name already exists")
        self.state["volumes"].append(name)
        self.persist()
        self.docker(
            "volume",
            "create",
            "--label",
            "loomplane.acceptance=" + self.state["run_id"],
            name,
        )
        if self.owned("volume", name) is None:
            raise RuntimeError("fixture volume creation was not observed")

    def setup(self):
        self.directory.mkdir(parents=True, exist_ok=True)
        self.directory.chmod(0o700)
        self.state = {
            "run_id": "lp-shared-" + uuid.uuid4().hex[:12],
            "daemon": self.docker("info", "--format", "{{.ID}}"),
            "containers": [],
            "volumes": [],
            "network": None,
        }
        self.persist()
        name = self.state["run_id"]
        self.state["network_intent"] = name
        self.persist()
        network = self.docker(
            "network",
            "create",
            "--internal",
            "--label",
            "loomplane.acceptance=" + name,
            name,
        )
        self.state["network"] = network
        self.persist()
        network_info = json.loads(self.docker("network", "inspect", network))[0]
        subnet = network_info["IPAM"]["Config"][0]["Subnet"]
        base = subnet.split("/")[0].rsplit(".", 1)[0]
        config = {
            "gateway": base + ".10",
            "pebble": base + ".11",
            "host1": base + ".21",
            "host2": base + ".22",
        }
        self.state["addresses"] = config
        self.persist()
        save(self.directory / "config.json", config)
        save(self.directory / "records.json", {})
        command(
            [
                "openssl",
                "req",
                "-x509",
                "-newkey",
                "rsa:2048",
                "-nodes",
                "-days",
                "7",
                "-subj",
                "/CN=Loomplane isolated acceptance transport",
                "-addext",
                "subjectAltName=IP:"
                + config["pebble"]
                + ",DNS:acme-v02.api.letsencrypt.org,DNS:acme-staging-v02.api.letsencrypt.org",
                "-addext",
                "basicConstraints=critical,CA:TRUE",
                "-keyout",
                str(self.directory / "transport.key"),
                "-out",
                str(self.directory / "transport.pem"),
            ]
        )
        (self.directory / "transport.key").chmod(0o600)
        pebble = {
            "pebble": {
                "listenAddress": "0.0.0.0:14000",
                "managementListenAddress": "0.0.0.0:15000",
                "certificate": "/fixture/transport.pem",
                "privateKey": "/fixture/transport.key",
                "httpPort": 80,
                "tlsPort": 443,
                "certificateValidityPeriod": 172800,
            }
        }
        save(self.directory / "pebble.json", pebble)
        mount = ["-v", str(self.directory) + ":/fixture", "--network", network]
        self.create(
            "gateway",
            "python:3.13-alpine",
            [
                *mount,
                "--ip",
                config["gateway"],
                "-v",
                str(HERE / "fixture.py") + ":/fixture.py:ro",
            ],
            ["python", "/fixture.py"],
        )
        self.create(
            "pebble",
            PEBBLE,
            [
                *mount,
                "--ip",
                config["pebble"],
                "-e",
                "PEBBLE_VA_NOSLEEP=1",
                "-e",
                "PEBBLE_AUTHZREUSE=0",
            ],
            [
                "-config",
                "/fixture/pebble.json",
                "-dnsserver",
                config["gateway"] + ":53",
            ],
        )
        command(
            [
                "ssh-keygen",
                "-q",
                "-t",
                "ed25519",
                "-N",
                "",
                "-f",
                str(self.directory / "ssh"),
            ]
        )
        # Package installation happens before the fixture joins its private network.
        for role in ("host1", "host2"):
            volume_name = name + "-" + role
            self.create_volume(volume_name)
            host = self.create(
                role,
                "docker:29-dind",
                [
                    "--privileged",
                    "--cpus",
                    "2",
                    "--memory",
                    "2g",
                    "-e",
                    "DOCKER_TLS_CERTDIR=",
                    "-v",
                    volume_name + ":/var/lib/docker",
                ],
                [
                    "dockerd",
                    "--host=unix:///var/run/docker.sock",
                    "--dns=" + config["gateway"],
                ],
            )
            self.docker(
                "exec",
                host,
                "sh",
                "-c",
                "apk add --no-cache openssh-server python3 iproute2 && ssh-keygen -A && passwd -d root && mkdir -p /root/.ssh",
                timeout=180,
            )
            for _ in range(60):
                try:
                    daemon = self.docker(
                        "exec", host, "docker", "info", "--format", "{{.ID}}"
                    )
                    break
                except subprocess.CalledProcessError:
                    time.sleep(1)
            else:
                raise RuntimeError("nested Docker did not become ready")
            self.docker(
                "exec",
                "-i",
                host,
                "sh",
                "-c",
                "cat > /root/.ssh/authorized_keys && chmod 600 /root/.ssh/authorized_keys && /usr/sbin/sshd -o PermitRootLogin=yes -o PasswordAuthentication=no -o AllowTcpForwarding=yes",
                data=(self.directory / "ssh.pub").read_bytes(),
            )
            host_key = self.docker(
                "exec", host, "cat", "/etc/ssh/ssh_host_ed25519_key.pub"
            )
            self.state.setdefault("hosts", {})[role] = {
                "container": host,
                "daemon": daemon,
                "address": config[role],
                "host_key": host_key,
            }
            self.persist()
            self.docker("exec", host, "docker", "pull", CADDY, timeout=180)
            self.docker("exec", host, "docker", "pull", WHOAMI, timeout=180)
            self.docker("network", "connect", "--ip", config[role], network, host)
            self.docker("network", "disconnect", "bridge", host)
        return self.state

    def preflight(self):
        config = self.state["addresses"]
        for host in self.state["hosts"].values():
            result = self.docker(
                "exec",
                host["container"],
                "docker",
                "run",
                "--rm",
                CADDY,
                "sh",
                "-c",
                "nslookup acme-v02.api.letsencrypt.org && nslookup acme-staging-v02.api.letsencrypt.org",
            )
            if config["gateway"] not in result:
                raise RuntimeError("nested DNS did not reach the private gateway")
        script = r"""import json, ssl, urllib.request, socket, struct
c=ssl.create_default_context(cafile='/fixture/transport.pem')
for host in ('acme-v02.api.letsencrypt.org','acme-staging-v02.api.letsencrypt.org'):
 gateway=json.load(open('/fixture/config.json'))['gateway']
 packet=struct.pack('!HHHHHH',42,256,1,0,0,0)+b''.join(bytes([len(p)])+p.encode() for p in host.split('.'))+b'\0'+struct.pack('!HH',1,1)
 with socket.create_connection((gateway,53),timeout=5) as sock:
  stream=sock.makefile('rwb');stream.write(struct.pack('!H',len(packet))+packet);stream.flush()
  size=struct.unpack('!H',stream.read(2))[0];response=stream.read(size);stream.close()
  assert response[-4:]==bytes(int(v) for v in gateway.split('.'))

 d=json.load(urllib.request.urlopen('https://'+host+'/directory',context=c))
 assert all(v.startswith('https://'+host+'/') for k,v in d.items() if k in ('newNonce','newAccount','newOrder','revokeCert','keyChange'))
 print(host, 'directory URLs verified')
root=urllib.request.urlopen('https://'+json.load(open('/fixture/config.json'))['pebble']+':15000/roots/0',context=c).read()
open('/fixture/issuance.pem','wb').write(root)
"""
        probe = self.create(
            "probe",
            "python:3.13-alpine",
            [
                "--network",
                self.state["network"],
                "--dns",
                config["gateway"],
                "-v",
                str(self.directory) + ":/fixture",
            ],
            ["python", "-c", script],
        )
        status = self.docker("wait", probe)
        if status != "0":
            raise RuntimeError(self.docker("logs", probe))
        self.state["gateway_preflight"] = "passed"
        self.persist()

    def combined(self):
        postgres = self.create(
            "postgres",
            "postgres:17-alpine",
            [
                "--network",
                self.state["network"],
                "--network-alias",
                "database",
                "--tmpfs",
                "/var/lib/postgresql/data",
                "-e",
                "POSTGRES_USER=fixture",
                "-e",
                "POSTGRES_PASSWORD=fixture",
                "-e",
                "POSTGRES_DB=fixture",
            ],
        )
        for _ in range(60):
            try:
                self.docker("exec", postgres, "pg_isready", "-U", "fixture")
                break
            except subprocess.CalledProcessError:
                time.sleep(1)
        else:
            raise RuntimeError("fixture PostgreSQL did not become ready")
        runner = self.create(
            "runner",
            "python:3.13-alpine",
            [
                "--network",
                self.state["network"],
                "--dns",
                self.state["addresses"]["gateway"],
                "-v",
                str(self.directory) + ":/fixture",
                "-v",
                str(ROOT) + ":/workspace:ro",
                "-w",
                "/workspace/tests/integration",
                "-e",
                "SSL_CERT_FILE=/fixture/issuance.pem",
                "-e",
                "LOOMPLANE_SHARED_CLOUD_STATE=/fixture/state.json",
                "-e",
                "LOOMPLANE_TEST_DATABASE_URL=postgres://fixture:fixture@database:5432/fixture?sslmode=disable",
                "-e",
                "GOMAXPROCS=2",
            ],
            [
                "/fixture/shared-cloud.test",
                "-test.v",
                "-test.run=^TestAcceptanceSharedCloudCombined$",
                "-test.timeout=15m",
            ],
        )
        try:
            status = self.docker("wait", runner, timeout=960)
        finally:
            captured = subprocess.run(
                ["docker", "logs", runner],
                capture_output=True,
                text=True,
                check=True,
                timeout=30,
            )
            log = captured.stdout + captured.stderr
            (self.directory / "combined.log").write_text(log)
        if status != "0":
            raise RuntimeError(
                "combined test failed; inspect " + str(self.directory / "combined.log")
            )
        self.state["combined"] = "passed"
        self.persist()

    def inspect(self, kind, identity):
        try:
            return json.loads(self.docker(kind, "inspect", identity))[0]
        except subprocess.CalledProcessError as error:
            detail = error.stderr or b""
            if isinstance(detail, bytes):
                detail = detail.decode(errors="replace")
            missing = (
                r"No such (?:object|container|volume|network):? "
                + re.escape(identity)
                + r"(?:\s|$)",
                r"get " + re.escape(identity) + r": no such volume(?:\s|$)",
                r"network " + re.escape(identity) + r" not found(?:\s|$)",
            )
            if any(re.search(pattern, detail) for pattern in missing):
                return None
            raise

    def owned(self, kind, identity):
        info = self.inspect(kind, identity)
        if info is not None:
            labels = (
                info.get("Config", {}).get("Labels")
                if kind == "container"
                else info.get("Labels")
            )
            if (labels or {}).get("loomplane.acceptance") != self.state["run_id"]:
                raise RuntimeError("cleanup owner mismatch")
        return info

    def capture_nested(self):
        for role, host in self.state.get("hosts", {}).items():
            if self.owned("container", host["container"]) is None:
                continue
            identity = self.docker(
                "exec", host["container"], "docker", "info", "--format", "{{.ID}}"
            )
            if identity != host["daemon"]:
                raise RuntimeError("nested daemon identity changed")
            containers = self.docker(
                "exec", host["container"], "docker", "ps", "-aq"
            ).splitlines()
            volumes = self.docker(
                "exec", host["container"], "docker", "volume", "ls", "-q"
            ).splitlines()
            networks = self.docker(
                "exec",
                host["container"],
                "docker",
                "network",
                "ls",
                "--filter",
                "type=custom",
                "-q",
                "--no-trunc",
            ).splitlines()
            if (
                not containers
                and not volumes
                and not networks
                and self.state.get("nested", {}).get(role, {}).get("cleaned")
            ):
                continue
            snapshot = {
                "containers": containers,
                "volumes": volumes,
                "networks": networks,
                "daemon": identity,
            }
            if containers:
                snapshot["container_details"] = json.loads(
                    self.docker(
                        "exec", host["container"], "docker", "inspect", *containers
                    )
                )
            self.state.setdefault("nested", {})[role] = snapshot
            self.persist()
            for container in containers:
                result = subprocess.run(
                    [
                        "docker",
                        "exec",
                        host["container"],
                        "docker",
                        "logs",
                        "--tail",
                        "100",
                        container,
                    ],
                    capture_output=True,
                    text=True,
                    timeout=30,
                    check=False,
                )
                (self.directory / (role + "-" + container + ".log")).write_text(
                    result.stdout + result.stderr
                )
            for container in containers:
                self.docker("exec", host["container"], "docker", "rm", "-f", container)
            for volume in volumes:
                self.docker("exec", host["container"], "docker", "volume", "rm", volume)
            for network in networks:
                self.docker(
                    "exec", host["container"], "docker", "network", "rm", network
                )
            if (
                self.docker("exec", host["container"], "docker", "ps", "-aq")
                or self.docker(
                    "exec", host["container"], "docker", "volume", "ls", "-q"
                )
                or self.docker(
                    "exec",
                    host["container"],
                    "docker",
                    "network",
                    "ls",
                    "--filter",
                    "type=custom",
                    "-q",
                )
            ):
                raise RuntimeError("nested resource cleanup incomplete")
            snapshot["cleaned"] = True
            self.persist()

    def cleanup(self):
        if not self.state:
            return  # Compilation failed before any infrastructure intent.
        if self.docker("info", "--format", "{{.ID}}") != self.state["daemon"]:
            raise RuntimeError("cleanup refused: parent Docker daemon changed")
        self.state["cleaned"] = False
        self.persist()
        # Recover response loss before stopping or deleting any resource.
        for resource in self.state["containers"]:
            info = self.owned("container", resource["id"] or resource["name"])
            if info is None:
                continue
            resource["id"] = info["Id"]
            self.persist()
            if resource["role"] == "runner":
                self.docker("stop", "-t", "15", resource["id"], timeout=30)
        self.capture_nested()
        for resource in reversed(self.state["containers"]):
            identity = resource["id"] or resource["name"]
            if self.owned("container", identity) is not None:
                self.docker("rm", "-f", identity)
                if self.inspect("container", identity) is not None:
                    raise RuntimeError("container remains after cleanup")
        for volume in self.state["volumes"]:
            if self.owned("volume", volume) is not None:
                self.docker("volume", "rm", volume)
                if self.inspect("volume", volume) is not None:
                    raise RuntimeError("volume remains after cleanup")
        network = self.state["network"] or self.state.get("network_intent")
        if network:
            info = self.owned("network", network)
            if info is not None:
                self.state["network"] = info["Id"]
                self.persist()
                self.docker("network", "rm", info["Id"])
                if self.inspect("network", info["Id"]) is not None:
                    raise RuntimeError("network remains after cleanup")
        self.state["cleaned"] = True
        self.persist()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--state", type=Path, required=True)
    parser.add_argument("--cleanup", action="store_true")
    parser.add_argument("--preflight-only", action="store_true")
    args = parser.parse_args()
    fixture = Fixture(args.state)
    if args.cleanup:
        fixture.cleanup()
        return
    if fixture.state:
        raise SystemExit("state already exists; clean it and use a fresh path")
    try:
        if not args.preflight_only:
            fixture.directory.mkdir(parents=True, exist_ok=True)
            build_env = dict(
                os.environ,
                GOOS="linux",
                GOARCH=go_architecture(
                    fixture.docker("info", "--format", "{{.Architecture}}")
                ),
                CGO_ENABLED="0",
                GOMAXPROCS="2",
            )
            subprocess.run(
                [
                    "go",
                    "test",
                    "-c",
                    "-tags=integration",
                    "-o",
                    str(fixture.directory / "shared-cloud.test"),
                    "./tests/integration/",
                ],
                cwd=ROOT,
                env=build_env,
                check=True,
                timeout=600,
            )
        fixture.setup()
        fixture.preflight()
        print("Private gateway and nested DNS preflight passed", flush=True)
        if not args.preflight_only:
            fixture.combined()
    except subprocess.CalledProcessError as error:
        print((error.stderr or b"").decode(errors="replace"), flush=True)
        raise
    finally:
        fixture.cleanup()


if __name__ == "__main__":
    main()
