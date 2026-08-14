FROM golang:1.26.6@sha256:640a234f4bea3e399c056b7b8f9c667c4939befae8db2f14e9785e16eccd4205 AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -o /out/knowl ./cmd/knowl

FROM alpine:3.22@sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce

ARG VERSION=dev
ARG REVISION=unknown
ARG CREATED=1970-01-01T00:00:00Z
ARG SOURCE=https://github.com/baldaworks/knowl

LABEL org.opencontainers.image.title="Knowl" \
      org.opencontainers.image.description="Durable project knowledge sidecar for agents" \
      org.opencontainers.image.source="${SOURCE}" \
      org.opencontainers.image.url="${SOURCE}" \
      org.opencontainers.image.documentation="${SOURCE}#readme" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.created="${CREATED}" \
      org.opencontainers.image.licenses="MIT"

RUN apk add --no-cache ca-certificates \
    && addgroup -S -g 65532 knowl \
    && adduser -S -D -h /var/lib/knowl -u 65532 -G knowl knowl

WORKDIR /var/lib/knowl

COPY --from=build /out/knowl /usr/local/bin/knowl
COPY deploy/sidecar/entrypoint.sh /usr/local/bin/knowl-entrypoint
COPY deploy/sidecar/knowl.yaml /etc/knowl/config.yaml

RUN chmod +x /usr/local/bin/knowl-entrypoint \
    && chown -R knowl:knowl /var/lib/knowl

EXPOSE 8080
VOLUME ["/var/lib/knowl"]
USER 65532:65532

HEALTHCHECK --interval=10s --timeout=3s --start-period=10s --retries=12 \
  CMD wget -qO- http://127.0.0.1:8080/readyz >/dev/null || exit 1

ENTRYPOINT ["/usr/local/bin/knowl-entrypoint"]
