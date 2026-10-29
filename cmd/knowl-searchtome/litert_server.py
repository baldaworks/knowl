"""Loopback-only LiteRT-LM adapter for the SearchTome benchmark."""

import hashlib
import json
from http.server import BaseHTTPRequestHandler, HTTPServer
from importlib.metadata import version
from pathlib import Path

from litert_lm import Backend, EmbeddingEngine, EmbeddingOptions, InputOverflowStrategy

MODEL = "litert-community/embeddinggemma-2-text-270m-litert-lm"
REVISION = "9be6e8b90982095dc05c2bd162e4b954ee4dbac7"
SHA256 = "2d079ee2f6f066b1f368e8d7c819f55214eaef1d0513b312321901f30ab286fb"
ROOT = Path(__file__).resolve().parents[2] / ".searchtome-benchmark"
MODEL_PATH = ROOT / "litert-model" / "embeddinggemma-2-text-270m.litertlm"

if version("litert-lm-api") != "0.18.0":
    raise SystemExit("LiteRT-LM version mismatch")
with MODEL_PATH.open("rb") as model_file:
    if hashlib.file_digest(model_file, "sha256").hexdigest() != SHA256:
        raise SystemExit("model checksum mismatch")

options = EmbeddingOptions(normalize=True, output_size=768,
                           input_overflow_strategy=InputOverflowStrategy.ERROR)


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_args):
        pass

    def reply(self, status, value):
        payload = json.dumps(value, separators=(",", ":"), allow_nan=False).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def do_GET(self):
        if self.path != "/info":
            self.reply(404, {})
            return
        self.reply(200, {"model_id": MODEL, "model_sha": REVISION,
                         "version": "litert-lm/0.18.0", "model_dtype": "int4-qat",
                         "model_file_sha256": SHA256, "auto_truncate": False})

    def do_POST(self):
        if self.path != "/v1/embeddings":
            self.reply(404, {})
            return
        length = int(self.headers.get("Content-Length", "0"))
        if not 0 < length <= 65536:
            self.reply(400, {})
            return
        try:
            request = json.loads(self.rfile.read(length))
            inputs = request["input"]
            if request["model"] != MODEL or not isinstance(inputs, list) or not 1 <= len(inputs) <= 4 or not all(isinstance(x, str) for x in inputs):
                raise ValueError("invalid input")
            data = []
            for index, text in enumerate(inputs):
                vector = engine.compute_embedding(text, options=options).embedding
                data.append({"index": index, "embedding": list(vector)})
            self.reply(200, {"model": MODEL, "data": data})
        except Exception:
            self.reply(500, {})


if __name__ == "__main__":
    with EmbeddingEngine(str(MODEL_PATH), backend=Backend.CPU(thread_count=2),
                         cache_dir=str(ROOT / "litert-cache")) as engine:
        HTTPServer(("127.0.0.1", 8082), Handler).serve_forever()
