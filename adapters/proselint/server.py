import json
import os
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from proselint.checks import __register__
from proselint.config import DEFAULT
from proselint.registry import CheckRegistry
from proselint.tools import LintFile

CheckRegistry().register_many(__register__)
MAX_BYTES = 256 * 1024


def utf16_offset(text: str, index: int) -> int:
    return len(text[:index].encode("utf-16-le")) // 2


class Handler(BaseHTTPRequestHandler):
    def log_message(self, fmt, *args):
        return

    def send_json(self, status: int, payload: dict):
        encoded = json.dumps(payload).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(encoded)))
        self.end_headers()
        self.wfile.write(encoded)

    def do_GET(self):
        if self.path == "/healthz":
            return self.send_json(200, {"ok": True})
        if self.path == "/v1/capabilities":
            return self.send_json(200, {"protocol": 1, "engine": "proselint", "version": "0.16.0", "languages": ["en"], "modes": ["standard"]})
        return self.send_json(404, {"error": "Not found"})

    def do_POST(self):
        if self.path != "/v1/check":
            return self.send_json(404, {"error": "Not found"})
        try:
            length = int(self.headers.get("Content-Length", "0"))
            if length <= 0 or length > MAX_BYTES:
                return self.send_json(413, {"error": "Request too large"})
            body = json.loads(self.rfile.read(length))
            text = body.get("text")
            if not isinstance(text, str):
                return self.send_json(400, {"error": "text must be a string"})
            results = LintFile("<request>", text).lint(DEFAULT)
            findings = []
            for result in results:
                # LintFile pads input with one leading newline.
                start_cp = max(0, result.check_result.span[0] - 1)
                end_cp = max(start_cp, result.check_result.span[1] - 1)
                replacement = result.check_result.replacements
                findings.append({
                    "start": utf16_offset(text, start_cp),
                    "end": utf16_offset(text, end_cp),
                    "kind": "style",
                    "message": result.check_result.message,
                    "replacements": [replacement] if replacement else [],
                    "ruleId": result.check_result.check_path,
                    "category": "proselint",
                })
            return self.send_json(200, {"findings": findings})
        except Exception:
            return self.send_json(400, {"error": "Invalid request"})


ThreadingHTTPServer(("0.0.0.0", int(os.environ.get("PORT", "8080"))), Handler).serve_forever()
