#!/usr/bin/env python3
"""A minimal OpenAI-compatible stub for the smoke test.

Some checks need a model call to complete before the thing they are
checking happens. The --limit accounting check is one: it runs AFTER
the call, so against an unreachable endpoint it never runs at all and
the exit code means something else entirely.

Writes its chosen port to argv[1] and serves until killed.
"""
import http.server
import sys


class Handler(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        self.rfile.read(int(self.headers.get("content-length", 0)))
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()
        self.wfile.write(b'data: {"choices":[{"delta":{"content":"ok"}}]}\n\n')
        self.wfile.write(b"data: [DONE]\n\n")

    def log_message(self, *args):
        pass


def main() -> int:
    srv = http.server.HTTPServer(("127.0.0.1", 0), Handler)
    with open(sys.argv[1], "w") as fh:
        fh.write(str(srv.server_port))
    srv.serve_forever()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
