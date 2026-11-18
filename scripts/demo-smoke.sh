#!/bin/sh
set -eu

base_url=${SHINYHUB_DEMO_URL:-https://demo.shinyhub.dev}
app_url=${SHINYHUB_DEMO_APP_URL:-https://apps.demo.shinyhub.dev}
max_attempts=${SHINYHUB_DEMO_SMOKE_ATTEMPTS:-60}
retry_delay=${SHINYHUB_DEMO_SMOKE_RETRY_DELAY:-5}
expected_version=${SHINYHUB_DEMO_EXPECTED_VERSION:-}
expected_deployment=${SHINYHUB_DEMO_EXPECTED_DEPLOYMENT:-}
phase=${1:-all}
case "$phase" in
  all | wait-for-rollout | verify) ;;
  *) echo 'usage: demo-smoke.sh [wait-for-rollout|verify]' >&2; exit 2 ;;
esac
if [ -n "$expected_deployment" ] && [ -z "$expected_version" ]; then
  echo 'The expected demo deployment also requires an expected version' >&2
  exit 2
fi

# Synthetic marker only: it links this run's HTTP requests to server telemetry.
# Use a wrapper so new probes cannot accidentally omit the marker.
curl() {
  if [ -n "${SHINYHUB_DEMO_SMOKE_ID:-}" ]; then
    command curl --header "X-ShinyHub-Demo-Smoke: $SHINYHUB_DEMO_SMOKE_ID" "$@"
  else
    command curl "$@"
  fi
}

# A rollout can replace the container after the initial wake. Status probes
# only report readiness. Use a browser-style navigation to wake the replacement
# so the Worker can forward the next probe and confirm it is healthy.
nudge() {
  curl --silent --show-error --output /dev/null \
    --connect-timeout 10 --max-time 30 \
    --header 'Sec-Fetch-Dest: document' \
    --header 'Sec-Fetch-Mode: navigate' \
    --header 'Accept: text/html,application/xhtml+xml' \
    "$base_url/" || true
}

check() {
  url=$1
  shift
  code=$(curl --silent --show-error --output /dev/null \
    --connect-timeout 10 --max-time 30 --write-out '%{http_code}' "$url") || code=000
  for expected in "$@"; do
    if [ "$code" = "$expected" ]; then
      printf '%s -> %s\n' "$url" "$code"
      return
    fi
  done
  echo "$url -> unexpected status $code" >&2
  exit 1
}

start_path=/__demo/start

# An entry page is never refused, whatever the container is doing. Asleep it is
# the start page, rendered at the edge; awake it is the real login page. This
# runs before the wake, which is the only moment the demo may still be asleep,
# but it asserts only what holds in both states so it cannot flake on a demo
# somebody is already using. When the edge does answer with the start page, the
# button is checked too: it is the sole way through for a request carrying no
# browser headers, which is exactly what this bare curl is.
entry() {
  headers=$(mktemp)
  body=$(mktemp)
  trap 'rm -f "$headers" "$body"' EXIT
  code=$(curl --silent --show-error --connect-timeout 10 --max-time 30 \
    --dump-header "$headers" --output "$body" --write-out '%{http_code}' "$base_url/") || code=000
  case "$code" in
    200 | 302 | 303) ;;
    *)
      echo "$base_url/ -> a bare request to an entry page must never be refused, got $code" >&2
      exit 1
      ;;
  esac
  if grep -qi '^x-shinyhub-demo-state: asleep' "$headers"; then
    if ! grep -q "$start_path" "$body"; then
      echo "$base_url/ -> start page does not offer the button that starts the demo" >&2
      exit 1
    fi
    printf '%s -> start page with a start button\n' "$base_url/"
  else
    printf '%s -> already awake\n' "$base_url/"
  fi
  rm -f "$headers" "$body"
  trap - EXIT
}

# The Worker spends a cold start only on a visitor opening the demo, so the
# smoke test arrives the way a browser does. A bare probe is answered from the
# edge while the container is asleep, by design. The navigation is repeated each
# round because it is the only thing that starts the container, and the first
# one can land before the deployment has propagated.
wake() {
  attempt=1
  while [ "$attempt" -le "$max_attempts" ]; do
    nudge
    if code=$(curl --silent --show-error --output /dev/null \
      --connect-timeout 10 --max-time 30 --write-out '%{http_code}' \
      "$base_url/__demo/ready"); then
      if [ "$code" = "204" ]; then
        printf '%s -> awake\n' "$base_url/"
        return
      fi
    fi
    if [ "$attempt" -lt "$max_attempts" ]; then
      sleep "$retry_delay"
    fi
    attempt=$((attempt + 1))
  done
  echo "$base_url/ -> demo container did not wake after $max_attempts attempts" >&2
  exit 1
}

release_matches() {
  server_info=$(curl --silent --show-error --fail --include --suppress-connect-headers \
    --connect-timeout 10 --max-time 30 "$base_url/api/server-info") || return 1
  case "$server_info" in
    *'"version":"'"$expected_version"'"'*) ;;
    *) return 1 ;;
  esac
  if [ -n "$expected_deployment" ]; then
    # Compare only response headers. An old image can serve the right version.
    printf '%s\n' "$server_info" | tr -d '\r' | sed '/^$/q' | \
      grep -Fxi "x-shinyhub-demo-deployment: $expected_deployment" >/dev/null
  fi
}

wait_for_rollout() (
  # The rollout may replace a healthy old image. Only begin the functional
  # checks once the requested release itself is serving.
  SHINYHUB_DEMO_SMOKE_ID=
  entry
  wake
  if [ -n "$expected_version" ]; then
    attempt=1
    until release_matches; do
      if [ "$attempt" -ge "$max_attempts" ]; then
        echo "$base_url/api/server-info -> expected version $expected_version${expected_deployment:+ and deployment $expected_deployment} after $max_attempts attempts" >&2
        exit 1
      fi
      nudge
      sleep "$retry_delay"
      attempt=$((attempt + 1))
    done
  fi
)

if [ "$phase" != verify ]; then
  wait_for_rollout
  [ "$phase" != wait-for-rollout ] || exit 0
fi

if [ -n "$expected_version" ]; then
  if ! release_matches; then
    echo "$base_url/api/server-info -> expected version $expected_version${expected_deployment:+ and deployment $expected_deployment} during verification" >&2
    exit 1
  fi
  printf '%s -> version %s%s\n' "$base_url/api/server-info" "$expected_version" "${expected_deployment:+, deployment $expected_deployment}"
fi

check "$base_url/healthz" 200
check "$base_url/__demo/ready" 204
check "$base_url/" 200 302
check "$base_url/login" 200

# The start button is the only way into a sleeping container for a request with
# no browser headers, so the route has to exist whatever state the demo is in.
# Awake it is a 303 onward; asleep it would be the wake page. A 404 or a 405
# here means the button on the start page leads nowhere and the demo can only be
# opened by a browser the edge happens to recognise.
start_code=$(curl --silent --show-error --output /dev/null \
  --connect-timeout 10 --max-time 30 --request POST --write-out '%{http_code}' \
  "$base_url$start_path") || start_code=000
case "$start_code" in
  200 | 303) printf '%s -> %s\n' "$base_url$start_path" "$start_code" ;;
  *)
    echo "$base_url$start_path -> unexpected status $start_code" >&2
    exit 1
    ;;
esac

login_html=$(curl --silent --show-error --fail "$base_url/login")
case "$login_html" in
  *'/__demo/assets/v1/login.css'*'/__demo/session'*'/__demo/assets/v1/login.js'*)
    printf '%s -> one-click entry assets\n' "$base_url/login"
    ;;
  *)
    echo "$base_url/login -> one-click entry markup missing" >&2
    exit 1
    ;;
esac
check "$base_url/__demo/assets/v1/login.css" 200
check "$base_url/__demo/assets/v1/login.js" 200

demo_cookie_jar=$(mktemp)
trap 'rm -f "$demo_cookie_jar"' EXIT
entry_code=$(curl --silent --show-error --output /dev/null \
  --cookie-jar "$demo_cookie_jar" --request POST --write-out '%{http_code}' \
  "$base_url/__demo/session")
if [ "$entry_code" != "303" ]; then
  echo "$base_url/__demo/session -> unexpected status $entry_code" >&2
  exit 1
fi
viewer_session=$(curl --silent --show-error --fail \
  --cookie "$demo_cookie_jar" "$base_url/api/auth/me")
case "$viewer_session" in
  *'"username":"demo-viewer"'*'"role":"viewer"'*'"display_name":"Demo Viewer"'*)
    printf '%s -> populated viewer session\n' "$base_url/__demo/session"
    ;;
  *)
    echo "$base_url/__demo/session -> unexpected session response" >&2
    exit 1
    ;;
esac
rm -f "$demo_cookie_jar"
trap - EXIT

check "$app_url/healthz" 200
check "$app_url/app/operations-dashboard/" 200
check "$app_url/app/r-shiny-gallery/" 200
check "$app_url/app/dash-demo/" 200
check "$app_url/app/streamlit-demo/" 200
check "$app_url/app/identity-demo/" 200
check "$app_url/app/bookmarking-demo/" 200
check "$app_url/app/agent-capabilities-demo/" 200

agent_status=$(curl --silent --show-error --fail \
  "$app_url/app/agent-capabilities-demo/agent-status")
case "$agent_status" in
  *'"mode":"Demo agent"'*) printf '%s -> scripted agent\n' "$app_url/app/agent-capabilities-demo/agent-status" ;;
  *) echo "public agent demo is not in scripted mode" >&2; exit 1 ;;
esac

agent_reply=$(curl --silent --show-error --fail \
  --header 'Content-Type: application/json' \
  --data '{"message":"Set the view to this year","thread_id":"public-smoke","view":"This week"}' \
  "$app_url/app/agent-capabilities-demo/chat")
case "$agent_reply" in
  *'"type":"view_changed","period":"This year"'*'"type":"done"'*)
    printf '%s -> scripted view change\n' "$app_url/app/agent-capabilities-demo/chat" ;;
  *) echo "public agent demo did not change the view" >&2; exit 1 ;;
esac

websocket_base=$(printf '%s' "$app_url" | sed 's,^https://,wss://,; s,^http://,ws://,')
node "$(dirname "$0")/demo-websocket-smoke.mjs" \
  "$websocket_base/app/operations-dashboard/websocket/" \
  "$websocket_base/app/bookmarking-demo/websocket/" \
  "$websocket_base/app/streamlit-demo/_stcore/stream"
