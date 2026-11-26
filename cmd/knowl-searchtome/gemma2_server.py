"""Local-only OpenAI-compatible text embeddings for the SearchTome benchmark."""

import json
from http.server import BaseHTTPRequestHandler, HTTPServer

import torch
from sentence_transformers import SentenceTransformer

MODEL = "google/embeddinggemma-2"
REVISION = "f9972ac22e696a105c482202646979e33e4b5a1e"
torch.set_num_threads(2)
model = SentenceTransformer(
    MODEL,
    revision=REVISION,
    device="cpu",
    model_kwargs={"torch_dtype": torch.float32},
    config_kwargs={"vision_config": None, "audio_config": None},
)


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
                         "version": "sentence-transformers/6.1.0",
                         "model_dtype": "float32", "auto_truncate": False})

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
            if any(len(model.tokenizer.encode(item, truncation=False)) > model.max_seq_length for item in inputs):
                self.reply(400, {})
                return
            vectors = model.encode(inputs, batch_size=len(inputs), normalize_embeddings=True,
                                   convert_to_numpy=True, show_progress_bar=False)
            self.reply(200, {"model": MODEL, "data": [
                {"index": index, "embedding": vector.tolist()}
                for index, vector in enumerate(vectors)
            ]})
        except Exception:
            self.reply(500, {})


if __name__ == "__main__":
    HTTPServer(("127.0.0.1", 8081), Handler).serve_forever()
