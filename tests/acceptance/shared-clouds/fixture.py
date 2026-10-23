#!/usr/bin/env python3
"""Private DNS and ACME transport for the shared-cloud acceptance network."""

import datetime
import http.client
import json
import socketserver
import ssl
import struct
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

ACME_HOSTS = {"acme-v02.api.letsencrypt.org", "acme-staging-v02.api.letsencrypt.org"}
DIRECTORY = Path("/fixture")
LOCK = threading.Lock()


def answer(packet, records, gateway):
    """Answer only fixture names. Unknown destinations receive NXDOMAIN."""
    offset, parts = 12, []
    while packet[offset]:
        size = packet[offset]
        offset += 1
        parts.append(packet[offset : offset + size].decode("ascii"))
        offset += size
    name = ".".join(parts).lower()
    kind, klass = struct.unpack("!HH", packet[offset + 1 : offset + 5])
    question = packet[12 : offset + 5]
    values = records.get(name, {})
    if name in ACME_HOSTS:
        values = {"A": [gateway]}
    encoded = []
    for value in values.get({1: "A", 16: "TXT"}.get(kind, ""), []):
        if kind == 1:
            data = bytes(int(part) for part in value.split("."))
        else:
            data = value.encode()
            data = bytes([len(data)]) + data
        encoded.append(
            b"\xc0\x0c" + struct.pack("!HHIH", kind, klass, 1, len(data)) + data
        )
    flags = 0x8580 if values else 0x8583
    return (
        packet[:2]
        + struct.pack("!HHHHH", flags, 1, len(encoded), 0, 0)
        + question
        + b"".join(encoded)
    )


class DNS(socketserver.BaseRequestHandler):
    def handle(self):
        packet, sock = self.request
        try:
            with LOCK:
                records = json.loads((DIRECTORY / "records.json").read_text())
            sock.sendto(answer(packet, records, CONFIG["gateway"]), self.client_address)
        except (IndexError, ValueError, UnicodeError, struct.error):
            return


class DNSTCP(socketserver.StreamRequestHandler):
    def handle(self):
        self.connection.settimeout(10)
        while header := self.rfile.read(2):
            if len(header) != 2:
                return
            size = struct.unpack("!H", header)[0]
            packet = self.rfile.read(size)
            if len(packet) != size:
                return
            try:
                with LOCK:
                    records = json.loads((DIRECTORY / "records.json").read_text())
                response = answer(packet, records, CONFIG["gateway"])
                self.wfile.write(struct.pack("!H", len(response)) + response)
                self.wfile.flush()
            except (IndexError, ValueError, UnicodeError, struct.error):
                return


class Control(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def do_POST(self):
        if self.path != "/records":
            self.send_error(404)
            return
        record = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        with LOCK:
            records = json.loads((DIRECTORY / "records.json").read_text())
            records[record["name"]] = record["values"]
            (DIRECTORY / "records.json").write_text(json.dumps(records))
            changed_at = datetime.datetime.now(datetime.UTC).isoformat()
        self.send_response(204)
        self.send_header("X-Fixture-Changed-At", changed_at)
        self.end_headers()


class Gateway(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def forward(self):
        host = self.headers.get("Host", "")
        path = self.path
        allowed = path in {
            "/directory",
            "/nonce-plz",
            "/sign-me-up",
            "/order-plz",
            "/revoke-cert",
            "/rollover-account-key",
        } or path.startswith(
            (
                "/authZ/",
                "/chalZ/",
                "/finalize-order/",
                "/certZ/",
                "/my-order/",
                "/my-account/",
                "/list-orderz/",
                "/draft-ietf-acme-ari-03/renewalInfo/",
            )
        )
        if (
            host not in ACME_HOSTS
            or not allowed
            or (path == "/directory" and self.command != "GET")
        ):
            self.send_error(403)
            return
        data = self.rfile.read(int(self.headers.get("Content-Length", "0")))
        context = ssl.create_default_context(cafile=str(DIRECTORY / "transport.pem"))
        conn = http.client.HTTPSConnection(
            CONFIG["pebble"], 14000, context=context, timeout=20
        )
        headers = {"Host": host, "X-Forwarded-Proto": "https"}
        for name in ("Content-Type", "Accept", "User-Agent"):
            if name in self.headers:
                headers[name] = self.headers[name]
        conn.request(
            self.command,
            "/dir" if path == "/directory" else path,
            body=data,
            headers=headers,
        )
        result = conn.getresponse()
        body = result.read()
        self.send_response(result.status)
        for name, value in result.getheaders():
            if name.lower() not in {
                "connection",
                "transfer-encoding",
                "content-length",
                "server",
                "date",
            }:
                self.send_header(name, value)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)
        conn.close()
        with LOCK, (DIRECTORY / "gateway.jsonl").open("a") as log:
            log.write(
                json.dumps(
                    {
                        "host": host,
                        "path": path,
                        "method": self.command,
                        "status": result.status,
                    }
                )
                + "\n"
            )

    do_GET = forward
    do_HEAD = forward
    do_POST = forward


if __name__ == "__main__":
    CONFIG = json.loads((DIRECTORY / "config.json").read_text())
    dns = socketserver.ThreadingUDPServer(("0.0.0.0", 53), DNS)
    threading.Thread(target=dns.serve_forever, daemon=True).start()
    tcp = socketserver.ThreadingTCPServer(("0.0.0.0", 53), DNSTCP)
    threading.Thread(target=tcp.serve_forever, daemon=True).start()
    control = ThreadingHTTPServer(("0.0.0.0", 8055), Control)
    threading.Thread(target=control.serve_forever, daemon=True).start()
    gateway = ThreadingHTTPServer(("0.0.0.0", 443), Gateway)
    tls = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    tls.load_cert_chain(DIRECTORY / "transport.pem", DIRECTORY / "transport.key")
    gateway.socket = tls.wrap_socket(gateway.socket, server_side=True)
    gateway.serve_forever()
