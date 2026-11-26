#!/bin/sh
# T084 (FR-019, FR-020, FR-021): rollback check of 002 against the previous release.
# Usage: sh scripts/perf/rollback-check.sh <old binary> <new binary>
#
# Runs the NEW binary, then the OLD one, then the NEW one again, one after the other, on ONE
# SQLite DB (loopback only, a free port, a fresh run dir under $ROLLBACK_RUN_DIR or mktemp):
#   1. new: first start creates the admin; the password is replaced; an app, an API token and
#      a Telegram setting are written; GET /admin/api/v1/apps = 200.
#   2. old (rollback): starts on the DB the new binary used; the session cookie of step 1 and
#      a fresh login both work; GET /admin/api/v1/apps = 200 and lists the app of step 1.
#   3. new (roll forward): starts again on the same DB; GET /admin/api/v1/apps = 200 and lists
#      the app; the 002 events endpoint answers (protocol 3 hello needs a WebSocket client, so
#      only the HTTP side is checked: a request without a session is refused).
# Exit 0 when every step is healthy; otherwise exit 1 with the failing step. Every server is
# stopped on exit (trap). No secret is printed (the passwords stay in the run dir).
set -u

OLD_BIN=${1:?usage: rollback-check.sh <old binary> <new binary>}
NEW_BIN=${2:?usage: rollback-check.sh <old binary> <new binary>}
for b in "$OLD_BIN" "$NEW_BIN"; do
  [ -x "$b" ] || { echo "rollback-check: $b is not an executable file" >&2; exit 1; }
done
OLD_BIN=$(cd "$(dirname "$OLD_BIN")" && pwd)/$(basename "$OLD_BIN")
NEW_BIN=$(cd "$(dirname "$NEW_BIN")" && pwd)/$(basename "$NEW_BIN")

RUN_DIR=${ROLLBACK_RUN_DIR:-}
OWN_DIR=0
if [ -z "$RUN_DIR" ]; then
  RUN_DIR=$(mktemp -d "${TMPDIR:-/tmp}/rollback-check.XXXXXX")
  OWN_DIR=1 # removed after a successful run (kept on failure for its logs)
else
  mkdir -p "$RUN_DIR"
fi
umask 077
JAR="$RUN_DIR/cookies.txt"
PID=""

stop_server() {
  if [ -n "$PID" ] && kill -0 "$PID" 2>/dev/null; then
    kill -TERM "$PID" 2>/dev/null
    i=0
    while kill -0 "$PID" 2>/dev/null && [ $i -lt 50 ]; do sleep 0.1; i=$((i + 1)); done
    kill -0 "$PID" 2>/dev/null && kill -KILL "$PID" 2>/dev/null
    wait "$PID" 2>/dev/null
  fi
  PID=""
}
trap stop_server EXIT INT TERM

fail() {
  echo "rollback-check: FAIL: $*" >&2
  echo "rollback-check: server logs in $RUN_DIR" >&2
  exit 1
}

PORT=$(python3 -I -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()') || fail "no free port"
BASE="http://127.0.0.1:$PORT"

start_server() { # <label> <binary>
  label=$1
  # The retired account variables must not leak in from the caller's shell.
  env -u ADMIN_USERNAME -u ADMIN_PASSWORD \
    LISTEN_ADDR="127.0.0.1:$PORT" DEPLOY_QUEUE_DB_PATH="$RUN_DIR/db.sqlite" COOKIE_SECURE=false \
    WEBHOOK_SECRET=rollback-secret-rollback-secret-xx DEPLOY_BINARY_PATH="$RUN_DIR/boot-bin" \
    DEPLOY_SERVICE_NAME=rollback-boot.service GITHUB_REPO=example/rollback ARTIFACT_NAME=rollback \
    "$2" >>"$RUN_DIR/server-$label.log" 2>&1 &
  PID=$!
  i=0
  while :; do
    code=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/admin/api/v1/session" 2>/dev/null)
    [ "$code" = 401 ] && break
    kill -0 "$PID" 2>/dev/null || fail "$label: the server exited during startup (see server-$label.log)"
    i=$((i + 1))
    [ $i -lt 200 ] || fail "$label: not ready after 20 s"
    sleep 0.1
  done
  echo "rollback-check: $label: up (pid $PID, $BASE)"
}

# api <method> <path> [json body] -> prints the HTTP status; the body goes to $RUN_DIR/last.json
api() {
  if [ $# -ge 3 ]; then
    curl -s -o "$RUN_DIR/last.json" -w '%{http_code}' -b "$JAR" -c "$JAR" -X "$1" \
      -H 'Content-Type: application/json' -H 'X-Admin-Request: true' -H 'Sec-Fetch-Site: same-origin' -H "Origin: $BASE" \
      --data-binary "$3" "$BASE$2"
  else
    curl -s -o "$RUN_DIR/last.json" -w '%{http_code}' -b "$JAR" -c "$JAR" -X "$1" \
      -H 'X-Admin-Request: true' -H 'Sec-Fetch-Site: same-origin' -H "Origin: $BASE" "$BASE$2"
  fi
}

json_has() { # <python expression over d> -> exit 0 when true
  python3 -I -c "import json,sys; d=json.load(open(sys.argv[1])); sys.exit(0 if ($1) else 1)" "$RUN_DIR/last.json"
}

NEWPW=rollback-check-password-123

# 1. new binary: fresh DB
start_server new-1 "$NEW_BIN"
[ -s "$RUN_DIR/initial-admin-password" ] || fail "new-1: no initial-admin-password"
INITIAL=$(cat "$RUN_DIR/initial-admin-password")
body=$(python3 -I -c 'import json,sys; print(json.dumps({"username": "admin", "password": sys.argv[1]}))' "$INITIAL")
[ "$(api POST /admin/api/v1/session "$body")" = 200 ] || fail "new-1: login with the initial password"
body=$(python3 -I -c 'import json,sys; print(json.dumps({"currentPassword": sys.argv[1], "newPassword": sys.argv[2]}))' "$INITIAL" "$NEWPW")
[ "$(api POST /admin/api/v1/account/password "$body")" = 200 ] || fail "new-1: password change"
[ "$(api POST /admin/api/v1/apps '{"name":"rollback-app","binaryPath":"/opt/rollback/app","serviceName":"rollback-app.service","githubRepo":"example/rollback-app","artifactName":"rollback-app"}')" = 201 ] || fail "new-1: create app"
[ "$(api POST /admin/api/v1/tokens '{"name":"rollback-token"}')" = 201 ] || fail "new-1: create token"
[ "$(api POST /admin/api/v1/telegram '{"appId":0,"appHash":"","botToken":"","chatUsername":"@rollback","enabled":false}')" = 200 ] || fail "new-1: save Telegram"
[ "$(api GET /admin/api/v1/apps)" = 200 ] || fail "new-1: GET /admin/api/v1/apps"
json_has 'any(a["name"] == "rollback-app" for a in d["apps"])' || fail "new-1: the app is not listed"
echo "rollback-check: new-1: GET /admin/api/v1/apps = 200 (app, token, Telegram written)"
stop_server

# 2. old binary on the DB of the new one (rollback)
start_server old "$OLD_BIN"
[ "$(api GET /admin/api/v1/apps)" = 200 ] || fail "old: GET /admin/api/v1/apps with the session of the new binary"
json_has 'any(a["name"] == "rollback-app" for a in d["apps"])' || fail "old: the app written by the new binary is not listed"
rm -f "$JAR"
body=$(python3 -I -c 'import json,sys; print(json.dumps({"username": "admin", "password": sys.argv[1]}))' "$NEWPW")
[ "$(api POST /admin/api/v1/session "$body")" = 200 ] || fail "old: login with the password set by the new binary"
[ "$(api GET /admin/api/v1/apps)" = 200 ] || fail "old: GET /admin/api/v1/apps after a fresh login"
[ "$(api GET /admin/api/v1/tokens)" = 200 ] || fail "old: GET /admin/api/v1/tokens"
json_has 'any(t["name"] == "rollback-token" for t in d["tokens"])' || fail "old: the token written by the new binary is not listed"
[ "$(api GET /admin/api/v1/telegram)" = 200 ] || fail "old: GET /admin/api/v1/telegram"
[ "$(api POST /admin/api/v1/apps '{"name":"rollback-app-old","binaryPath":"/opt/rollback/old","serviceName":"rollback-old.service","githubRepo":"example/rollback-old","artifactName":"rollback-old"}')" = 201 ] || fail "old: create app"
echo "rollback-check: old: GET /admin/api/v1/apps = 200 on the new binary's DB (app, token, Telegram readable; write ok)"
stop_server

# 3. new binary again (roll forward)
start_server new-2 "$NEW_BIN"
[ "$(api GET /admin/api/v1/apps)" = 200 ] || fail "new-2: GET /admin/api/v1/apps"
json_has 'sorted(a["name"] for a in d["apps"] if a["name"].startswith("rollback-app")) == ["rollback-app", "rollback-app-old"]' || fail "new-2: the apps of both binaries are not listed"
code=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/admin/events/v1/ws?protocol=3")
case "$code" in 401 | 403) ;; *) fail "new-2: /admin/events/v1/ws without a session answered $code, want 401/403" ;; esac
echo "rollback-check: new-2: GET /admin/api/v1/apps = 200, both apps listed; events endpoint refuses without a session ($code)"
stop_server

echo "rollback-check: OK (new -> old -> new on one DB: $RUN_DIR)"
if [ "$OWN_DIR" = 1 ]; then
  rm -r "$RUN_DIR" && echo "rollback-check: removed $RUN_DIR"
fi
exit 0
