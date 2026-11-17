#!/usr/bin/env bash
# Builds (once) and starts a MinIO server for the S3 repository tests (ADR-007 §4), on 127.0.0.1:19000 with a
# throw-away data directory, and prints the environment the tests read (the data directory too: a test damages a
# stored object on MinIO's own disk). MinIO publishes no binaries or images any more,
# so it is built from source at a pinned commit (go install; needs Go and network access).
#
#   eval "$(scripts/test-minio.sh)"   # then: make check   (or go test ./internal/diskstore ./internal/server)
#   scripts/test-minio.sh stop
set -euo pipefail

MINIO_VERSION=v0.0.0-20260212201848-7aac2a2c5b7c # github.com/minio/minio at 7aac2a2c (2026-02-12)
BIN_DIR=${GAIA_MINIO_BIN:-${XDG_CACHE_HOME:-$HOME/.cache}/gaia/minio-$MINIO_VERSION}
ADDR=127.0.0.1:19000
USER_=gaiatest
PASS_=gaiatest-secret-123 # a test value for a server that only listens on loopback
RUN_DIR=${TMPDIR:-/tmp}/gaia-test-minio

if [[ ${1:-} == stop ]]; then
	if [[ -f $RUN_DIR/pid ]]; then
		kill "$(cat "$RUN_DIR/pid")" 2>/dev/null || true
	fi
	rm -rf "$RUN_DIR"
	exit 0
fi

if [[ ! -x $BIN_DIR/minio ]]; then
	echo "building MinIO $MINIO_VERSION into $BIN_DIR" >&2
	GOBIN=$BIN_DIR CGO_ENABLED=0 go install "github.com/minio/minio@$MINIO_VERSION" >&2
fi
if ! curl -sf "http://$ADDR/minio/health/live" >/dev/null; then
	rm -rf "$RUN_DIR"
	mkdir -p "$RUN_DIR/data"
	MINIO_ROOT_USER=$USER_ MINIO_ROOT_PASSWORD=$PASS_ nohup "$BIN_DIR/minio" server --address "$ADDR" --console-address 127.0.0.1:19001 \
		"$RUN_DIR/data" >"$RUN_DIR/minio.log" 2>&1 &
	echo $! >"$RUN_DIR/pid"
	for _ in $(seq 60); do
		curl -sf "http://$ADDR/minio/health/live" >/dev/null && break
		sleep 1
	done
	curl -sf "http://$ADDR/minio/health/live" >/dev/null || {
		cat "$RUN_DIR/minio.log" >&2
		exit 1
	}
fi
echo "export GAIA_TEST_S3=http://$USER_:$PASS_@$ADDR/gaia-test"
echo "export GAIA_TEST_S3_DIR=$RUN_DIR/data"
