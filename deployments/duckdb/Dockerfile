FROM --platform=$BUILDPLATFORM golang:1.26-alpine@sha256:a2012b4a1d1f9f7e9f44e0d39b805a901e856b1dd80b4e6ee4338db547050308 AS build
ARG TARGETARCH
WORKDIR /src
COPY cmd/duckdb-runtime/main.go ./main.go
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -trimpath -ldflags='-s -w' -o /loomplane-duckdb ./main.go
RUN apk add --no-cache curl
RUN set -eu; \
    case "$TARGETARCH" in \
      amd64) httpfs=77e6c429aa04f25da7962572f12b19113e0ce46080fa1365ad66017aadcdb68d; quack=9c893bd543ec2c5253d0c47d5614f90a1bfa1791c735dc6bbcea4e8bd3811a97 ;; \
      arm64) httpfs=53f7aea1a0448ea62ef9b63f95da37c5766070084f14f14110e275e76382e7b2; quack=f3e30e7533f53e2b935ec29b59f87cb26ec3afaacb9880e9072426a6d16ced9e ;; \
      *) exit 1 ;; \
    esac; \
    mkdir -p /extensions/v2.0.0/linux_${TARGETARCH}; \
    for extension in httpfs quack; do \
      curl --fail --silent --show-error --retry 3 "https://extensions.duckdb.org/v2.0.0/linux_${TARGETARCH}/${extension}.duckdb_extension.gz" -o /tmp/extension.gz; \
      gzip -dc /tmp/extension.gz > "/extensions/v2.0.0/linux_${TARGETARCH}/${extension}.duckdb_extension"; \
    done; \
    printf '%s  %s\n' "$httpfs" "/extensions/v2.0.0/linux_${TARGETARCH}/httpfs.duckdb_extension" "$quack" "/extensions/v2.0.0/linux_${TARGETARCH}/quack.duckdb_extension" | sha256sum -c -

FROM duckdb/duckdb@sha256:453caf4f1c445ee8363acaa6cd383751396d2eae0d02f362ada64d87267a4221
COPY --from=build /loomplane-duckdb /loomplane-duckdb
COPY --from=build /extensions/ /opt/duckdb/extensions/
RUN --network=none ["/duckdb", "-batch", "-bail", "-c", "SET extension_directories=['/opt/duckdb/extensions']; LOAD httpfs; LOAD quack;"]
ENTRYPOINT ["/loomplane-duckdb"]
CMD []
