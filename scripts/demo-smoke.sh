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

check "$base_url/healthz" 200
check "$base_url/" 200 302
check "$base_url/login" 200

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

websocket_base=$(printf '%s' "$app_url" | sed 's,^https://,wss://,; s,^http://,ws://,')
node "$(dirname "$0")/demo-websocket-smoke.mjs" \
  "$websocket_base/app/operations-dashboard/websocket/" \
  "$websocket_base/app/streamlit-demo/_stcore/stream"
