FROM golang:1.26.5 AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -o /out/knowl ./cmd/knowl

FROM alpine:3.22

RUN apk add --no-cache ca-certificates

WORKDIR /var/lib/knowl

COPY --from=build /out/knowl /usr/local/bin/knowl
COPY deploy/sidecar/entrypoint.sh /usr/local/bin/knowl-entrypoint
COPY deploy/sidecar/knowl.yaml /etc/knowl/config.yaml

RUN chmod +x /usr/local/bin/knowl-entrypoint

EXPOSE 8080
VOLUME ["/var/lib/knowl"]

ENTRYPOINT ["/usr/local/bin/knowl-entrypoint"]
