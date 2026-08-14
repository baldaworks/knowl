#!/bin/sh
set -eu

image="${1:-knowl:release-smoke}"
suffix="$(date +%s)-$$"
container="knowl-smoke-${suffix}"
volume="knowl-smoke-${suffix}"
operator_token="knowl-smoke-token-${suffix}"
expected_version="${KNOWL_SMOKE_EXPECT_VERSION:-}"
deadline_seconds="${KNOWL_SMOKE_DEADLINE_SECONDS:-60}"

cleanup() {
  docker rm -f "$container" >/dev/null 2>&1 || true
  docker volume rm "$volume" >/dev/null 2>&1 || true
}
trap cleanup EXIT HUP INT TERM

fail() {
  echo "sidecar smoke: $*" >&2
  exit 1
}

label() {
  docker image inspect --format "{{ index .Config.Labels \"$1\" }}" "$image"
}

user="$(docker image inspect --format '{{.Config.User}}' "$image")"
case "$user" in
  ""|0|0:0|root|root:root) fail "image user is not non-root: $user" ;;
esac

test "$(label org.opencontainers.image.source)" = "https://github.com/baldaworks/knowl" || fail "source label is invalid"
test -n "$(label org.opencontainers.image.revision)" || fail "revision label is empty"
test -n "$(label org.opencontainers.image.version)" || fail "version label is empty"
if test -n "$expected_version"; then
  test "$(label org.opencontainers.image.version)" = "$expected_version" || fail "version label does not match $expected_version"
fi

docker volume create "$volume" >/dev/null

start_container() {
  docker run -d \
    --name "$container" \
    --env KNOWL_OPERATOR_TOKEN="$operator_token" \
    --publish 127.0.0.1::8080 \
    --volume "$volume:/var/lib/knowl" \
    "$image" >/dev/null
}

wait_ready() {
  port="$(docker port "$container" 8080/tcp | sed -n 's/.*://p' | head -n 1)"
  test -n "$port" || fail "Docker did not publish port 8080"
  base_url="http://127.0.0.1:${port}"
  deadline="$(( $(date +%s) + deadline_seconds ))"
  while test "$(date +%s)" -lt "$deadline"; do
    if curl -fsS "$base_url/readyz" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  docker logs "$container" >&2 || true
  fail "sidecar did not become ready within ${deadline_seconds}s"
}

status_code() {
  curl -sS -o /dev/null -w '%{http_code}' "$@"
}

verify_surfaces() {
  test "$(status_code "$base_url/healthz")" = "200" || fail "healthz is not public"
  test "$(status_code "$base_url/readyz")" = "200" || fail "readyz is not public"
  test "$(status_code "$base_url/v1/retrieve?query=session")" = "401" || fail "HTTP retrieve accepted a missing token"
  test "$(status_code "$base_url/mcp")" = "401" || fail "MCP accepted a missing token"
  test "$(status_code -H "Authorization: Bearer $operator_token" "$base_url/v1/retrieve?query=session")" = "200" || fail "authenticated retrieve failed"
}

start_container
wait_ready
verify_surfaces

docker rm -f "$container" >/dev/null
start_container
wait_ready
verify_surfaces

echo "sidecar smoke passed for $image as user $user"
