#!/usr/bin/env bash
# Run scripts/wait-pypi.sh against simple indexes served from a local
# directory: it must return once every wheel and the sdist are listed, give up
# naming the missing file when one is absent, and refuse a body that is not
# simple-index JSON instead of reading it as "nothing missing".
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WAIT="${ROOT}/scripts/wait-pypi.sh"
WORK="$(mktemp -d)"
SERVER_PID=""

cleanup() {
  if [ -n "${SERVER_PID}" ]; then
    kill "${SERVER_PID}" 2>/dev/null || true
  fi
  rm -rf "${WORK}"
}
trap cleanup EXIT

fail() {
  echo "WAIT-PYPI E2E FAIL: $*" >&2
  exit 1
}

V=9.9.1
ALL=(
  "shinyhub-${V}-py3-none-manylinux_2_17_x86_64.whl"
  "shinyhub-${V}-py3-none-manylinux_2_17_aarch64.whl"
  "shinyhub-${V}-py3-none-macosx_11_0_x86_64.whl"
  "shinyhub-${V}-py3-none-macosx_11_0_arm64.whl"
  "shinyhub-${V}.tar.gz"
)

# index <file> <filename...>: write a simple-index JSON body listing the files.
index() {
  local out="$1"
  shift
  python3 -I -c '
import json, sys
files = [{"filename": n, "url": n, "hashes": {}} for n in sys.argv[2:]]
json.dump({"meta": {"api-version": "1.1"}, "name": "shinyhub", "files": files}, open(sys.argv[1], "w"))
' "${out}" "$@"
}

mkdir -p "${WORK}/srv"
index "${WORK}/srv/complete.json" "${ALL[@]}" "shinyhub-9.9.0.tar.gz"
index "${WORK}/srv/partial.json" "${ALL[0]}" "${ALL[1]}" "${ALL[2]}" "${ALL[4]}"
printf '<!DOCTYPE html><html><body>maintenance</body></html>\n' > "${WORK}/srv/page.html"

python3 -I - "${WORK}/srv" "${WORK}/port" <<'PY' &
import functools, http.server, os, sys, time

class Quiet(http.server.SimpleHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def do_GET(self):
        # /stall.json accepts the request and never answers in time.
        if self.path == "/stall.json":
            time.sleep(120)
            return
        super().do_GET()

handler = functools.partial(Quiet, directory=sys.argv[1])
server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), handler)
with open(sys.argv[2] + ".tmp", "w") as f:
    f.write(str(server.server_address[1]))
os.rename(sys.argv[2] + ".tmp", sys.argv[2])
server.serve_forever()
PY
SERVER_PID=$!
for _ in $(seq 1 100); do
  [ -s "${WORK}/port" ] && break
  sleep 0.05
done
[ -s "${WORK}/port" ] || fail "fixture server did not start"
BASE="http://127.0.0.1:$(cat "${WORK}/port")"

# run <name> <index path>: wait with a short budget, recording status and output.
run() {
  set +e
  WAIT_PYPI_INDEX="${BASE}/$2" WAIT_PYPI_TIMEOUT=1 WAIT_PYPI_INTERVAL=1 \
    "${WAIT}" "v${V}" > "${WORK}/$1.out" 2>&1
  STATUS=$?
  set -e
}

run complete complete.json
[ "${STATUS}" -eq 0 ] || fail "a complete index was not accepted:
$(cat "${WORK}/complete.out")"

run partial partial.json
[ "${STATUS}" -ne 0 ] || fail "an index missing a wheel was accepted"
grep -q "  ${ALL[3]}$" "${WORK}/partial.out" \
  || fail "the missing wheel was not named:
$(cat "${WORK}/partial.out")"
if grep -q "${ALL[0]}" "${WORK}/partial.out"; then
  fail "a listed wheel was reported missing:
$(cat "${WORK}/partial.out")"
fi

run html page.html
[ "${STATUS}" -ne 0 ] || fail "a non-JSON index body was accepted"
grep -q "did not answer with a simple-index JSON body" "${WORK}/html.out" \
  || fail "a non-JSON body was refused without saying why:
$(cat "${WORK}/html.out")"

run absent nowhere.json
[ "${STATUS}" -ne 0 ] || fail "an index answering 404 was accepted"
grep -q "the index answered: 404" "${WORK}/absent.out" \
  || fail "a 404 was refused without saying why:
$(cat "${WORK}/absent.out")"

# An index that accepts the request and then stalls must not stretch the
# wait past its budget.
started=${SECONDS}
run stall stall.json
elapsed=$(( SECONDS - started ))
[ "${STATUS}" -ne 0 ] || fail "a stalled index was accepted"
[ "${elapsed}" -le 10 ] || fail "a stalled index held a 1s wait for ${elapsed}s"
grep -q "gave up after 1s" "${WORK}/stall.out" \
  || fail "a stalled index was refused without saying why:
$(cat "${WORK}/stall.out")"

echo "wait-pypi e2e: ok"
