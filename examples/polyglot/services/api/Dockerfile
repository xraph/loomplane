FROM golang:1.26 AS build
WORKDIR /src
COPY . .
RUN go build -o /out/api .

FROM gcr.io/distroless/static
COPY --from=build /out/api /api
EXPOSE 8080
ENTRYPOINT ["/api"]
