#!/usr/bin/env python3
"""Scripted OpenAI-compatible model for end-to-end runs without a GPU.

Serves only POST /v1/chat/completions: the harness's runtime probe also
POSTs (Ollama's /api/show), and answering that would eat a script step.

argv[1] = port file, argv[2] = request log (one JSON body per line),
argv[3] = script file: JSON list of steps, each either
{"tool": name, "args": {...}} or {"text": "..."}; an optional "before"
is a shell command run before that step is answered.
"""
import http.server
import json
import subprocess
import sys

PORT_FILE, LOG, SCRIPT = sys.argv[1], sys.argv[2], sys.argv[3]
STEPS = json.load(open(SCRIPT))
n = 0


class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def do_GET(self):
        self.send_response(404)
        self.end_headers()

    def do_POST(self):
        global n
        if self.path != "/v1/chat/completions":
            self.rfile.read(int(self.headers.get("content-length", 0)))
            self.send_response(404)
            self.end_headers()
            return
        body = self.rfile.read(int(self.headers.get("content-length", 0)))
        with open(LOG, "ab") as fh:
            fh.write(body.replace(b"\n", b" ") + b"\n")
        if n >= len(STEPS):
            self.send_error(500, "script exhausted")
            return
        step = STEPS[n]
        n += 1
        if "before" in step:
            # Something the run must find changed when this answer
            # arrives — a server restarted between two tool calls.
            subprocess.run(step["before"], shell=True, check=True)
        if "tool" in step:
            delta = {"tool_calls": [{"index": 0, "id": f"call_rt_{n}",
                                     "function": {"name": step["tool"],
                                                  "arguments": json.dumps(step["args"])}}]}
        else:
            delta = {"content": step["text"]}
        out = "data: " + json.dumps({"choices": [{"delta": delta}]}) + "\n\ndata: [DONE]\n\n"
        raw = out.encode()
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)


srv = http.server.HTTPServer(("127.0.0.1", 0), Handler)
open(PORT_FILE, "w").write(str(srv.server_port))
srv.serve_forever()
