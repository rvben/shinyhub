#!/bin/sh
set -eu

base_url=${SHINYHUB_DEMO_URL:-https://demo.shinyhub.dev}
app_url=${SHINYHUB_DEMO_APP_URL:-https://apps.demo.shinyhub.dev}
max_attempts=${SHINYHUB_DEMO_SMOKE_ATTEMPTS:-60}
retry_delay=${SHINYHUB_DEMO_SMOKE_RETRY_DELAY:-5}

check() {
  url=$1
  shift
  attempt=1
  while [ "$attempt" -le "$max_attempts" ]; do
    if code=$(curl --silent --show-error --output /dev/null \
      --connect-timeout 10 --max-time 30 --write-out '%{http_code}' "$url"); then
      for expected in "$@"; do
        if [ "$code" = "$expected" ]; then
          printf '%s -> %s\n' "$url" "$code"
          return
        fi
      done
    else
      code=000
    fi
    if [ "$attempt" -lt "$max_attempts" ]; then
      sleep "$retry_delay"
    fi
    attempt=$((attempt + 1))
  done
  echo "$url -> unexpected status $code after $max_attempts attempts" >&2
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

entry

# The Worker spends a cold start only on a visitor opening the demo, so the
# smoke test arrives the way a browser does. A bare probe is answered from the
# edge while the container is asleep, by design. The navigation is repeated each
# round because it is the only thing that starts the container, and the first
# one can land before the deployment has propagated.
wake() {
  attempt=1
  while [ "$attempt" -le "$max_attempts" ]; do
    curl --silent --show-error --output /dev/null \
      --connect-timeout 10 --max-time 30 \
      --header 'Sec-Fetch-Dest: document' \
      --header 'Sec-Fetch-Mode: navigate' \
      --header 'Accept: text/html,application/xhtml+xml' \
      "$base_url/" || true
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

wake

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

if [ -n "${SHINYHUB_DEMO_EXPECTED_VERSION:-}" ]; then
  attempt=1
  server_info=
  while [ "$attempt" -le "$max_attempts" ]; do
    if server_info=$(curl --silent --show-error --fail \
      --connect-timeout 10 --max-time 30 "$base_url/api/server-info"); then
      case "$server_info" in
        *'"version":"'"$SHINYHUB_DEMO_EXPECTED_VERSION"'"'*)
          printf '%s -> version %s\n' "$base_url/api/server-info" "$SHINYHUB_DEMO_EXPECTED_VERSION"
          break
          ;;
      esac
    fi
    if [ "$attempt" -eq "$max_attempts" ]; then
      echo "$base_url/api/server-info -> expected version $SHINYHUB_DEMO_EXPECTED_VERSION after $max_attempts attempts, got $server_info" >&2
      exit 1
    fi
    sleep "$retry_delay"
    attempt=$((attempt + 1))
  done
fi

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

websocket_base=$(printf '%s' "$app_url" | sed 's,^https://,wss://,; s,^http://,ws://,')
node "$(dirname "$0")/demo-websocket-smoke.mjs" \
  "$websocket_base/app/operations-dashboard/websocket/" \
  "$websocket_base/app/bookmarking-demo/websocket/" \
  "$websocket_base/app/streamlit-demo/_stcore/stream"
