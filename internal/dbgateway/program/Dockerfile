# A Loomplane database gateway. Loomplane generates this directory; see main.go.
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /dbgateway .

# The gateway keeps its last-good snapshot under /tmp/dbgw by default. The
# nonroot image runs as uid 65532 and its /tmp is world-writable (1777), so
# the directory can be created at start without a volume.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /dbgateway /dbgateway
EXPOSE 8080
ENTRYPOINT ["/dbgateway"]
