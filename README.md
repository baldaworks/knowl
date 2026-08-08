# Knowl

Knowl is an independent, local-first LLM-maintained Markdown knowledge wiki.

The canonical workspace is made of immutable `raw/` source versions, a human-readable `wiki/`, and an operator-owned `schema.md`. The local `knowl` command is built in Go with Cobra and Viper.

## Quick start

```bash
go build ./cmd/knowl
./knowl init
./knowl validate
```

Configuration is loaded from `.config/knowl/config.yaml`, then `KNOWL_*` environment overrides, then explicit command flags. The initial shell only creates and validates the workspace; maintenance, storage adapters, and service surfaces are added by subsequent story tasks.
