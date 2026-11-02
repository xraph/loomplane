# DuckDB runtime image

Build both supported architectures locally:

```sh
scripts/build-duckdb-runtime.sh
```

The image contains the pinned DuckDB 2.0 CLI, checksum-verified httpfs and Quack extensions, and a static Go supervisor. It loads both extensions without network access during the build. The supervisor keeps the CLI's stdin open and sends `.quit` when the container receives TERM. Its `check` command makes an authenticated remote query with CA and hostname verification.

Mount the database volume at `/data`. Supply read-only files at `/etc/loomplane/token`, `/etc/loomplane/ca.pem`, `/etc/loomplane/node.pem` and `/etc/loomplane/node-key.pem`. The token is exactly 64 hexadecimal characters. The CA signing key stays in Loomplane's credential store.

After you qualify the built image, you can publish to an explicitly chosen registry:

```sh
scripts/build-duckdb-runtime.sh registry.example.com/team/duckdb:2.0-runtime1 --push
```

Record the actual manifest digest for each architecture and pass those pins to `recipes.DuckDBKind`. This constructor is unregistered until the packaged image is published and qualified. A local Docker image ID is not a registry manifest digest.

The native CLI supports the tested remote CRUD operations. The DuckDB Go binding's remote UPDATE and DELETE limitations still prevent using it as a complete Fabriq analytics sink. Publishing this image does not qualify that driver or enable catalog recovery.
