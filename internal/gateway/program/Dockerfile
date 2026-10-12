# A Loomplane gateway. Loomplane generates this directory; see main.go.
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /gateway .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /gateway /gateway
COPY routes.json /routes.json
EXPOSE 8080
ENTRYPOINT ["/gateway"]
