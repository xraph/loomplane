FROM golang:1.26-alpine AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/hello ./cmd/hello
FROM alpine:3.20.3@sha256:1e42bbe2508154c9126d48c2b8a75420c3544343bf86fd041fb7527e017a4b4a
COPY --from=build /out/hello /hello
EXPOSE 8080
ENTRYPOINT ["/hello"]
