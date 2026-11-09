"""Local push provider fixture. It never contacts Apple or Google."""

import json
from http.server import BaseHTTPRequestHandler, HTTPServer


class Provider(BaseHTTPRequestHandler):
    def log_message(self, *_args):
        pass

    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", "0")))
        if self.path == "/token":
            response = {
                "access_token": "local-fixture",
                "token_type": "Bearer",
                "expires_in": 3600,
            }
        elif self.path == "/v1/projects/smoke/messages:send":
            message = json.loads(body)["message"]
            assert self.headers["Authorization"] == "Bearer local-fixture"
            assert message["token"] == "smoke-token"
            assert message["data"]["sealed"]
            assert "notification" not in message
            print("accepted encrypted push", flush=True)
            response = {"name": "projects/smoke/messages/local-fixture"}
        else:
            self.send_error(404)
            return
        data = json.dumps(response).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)


HTTPServer(("0.0.0.0", 8080), Provider).serve_forever()
