#!/usr/bin/env bash
# Exercise completion installation through the real supported shells, not just
# generated-text assertions. Every startup file is installed twice, loaded by
# its shell, uninstalled, and compared byte-for-byte with its original content.
# shellcheck disable=SC2016 # child shells, not bash, expand these expressions
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="$(mktemp -d)"
COMPLETION_SHELLS="${SHINYHUB_COMPLETION_SHELLS:-bash zsh fish powershell}"

cleanup() {
  if [ -n "${E2E_KEEP:-}" ]; then
    echo "E2E_KEEP set; completion artifacts kept at ${WORK}" >&2
    return
  fi
  rm -rf "${WORK}"
}
trap cleanup EXIT

fail() {
  echo "SHELL COMPLETION E2E FAIL: $*" >&2
  exit 1
}

for shell in ${COMPLETION_SHELLS}; do
  case "${shell}" in
    bash|zsh|fish) command -v "${shell}" >/dev/null 2>&1 || fail "${shell} is required" ;;
    powershell)
      if command -v pwsh >/dev/null 2>&1; then
        POWERSHELL_BIN="$(command -v pwsh)"
      elif command -v powershell >/dev/null 2>&1; then
        POWERSHELL_BIN="$(command -v powershell)"
      else
        fail "pwsh or powershell is required"
      fi
      ;;
    *) fail "unsupported SHINYHUB_COMPLETION_SHELLS entry ${shell}" ;;
  esac
done

echo "==> preparing current CLI completion surface"
mkdir -p "${WORK}/bin"
if [ -n "${SHINYHUB_COMPLETION_BINARY:-}" ]; then
  [ -x "${SHINYHUB_COMPLETION_BINARY}" ] || fail "SHINYHUB_COMPLETION_BINARY is not executable"
  cp "${SHINYHUB_COMPLETION_BINARY}" "${WORK}/bin/shinyhub"
else
  GOWORK=off go build -o "${WORK}/bin/shinyhub" "${ROOT}/cmd/shinyhub" || fail "build current CLI"
fi
BIN="${WORK}/bin/shinyhub"
TEST_PATH="${WORK}/bin:${PATH}"

assert_one_block() {
  local path="$1"
  local count
  count="$(grep -Fc '# >>> shinyhub shell completion >>>' "${path}" || true)"
  [ "${count}" = "1" ] || fail "${path} has ${count} managed completion blocks"
}

assert_removed() {
  local path="$1"
  [ ! -e "${path}" ] || fail "completion script remains at ${path} after uninstall"
}

test_bash() {
  local test_home="${WORK}/bash-home"
  local config="${test_home}/config"
  local rc="${test_home}/.bashrc"
  local script="${config}/shinyhub/completions/shinyhub.bash"
  mkdir -p "${test_home}"
  printf 'export SHINYHUB_BASH_SENTINEL=preserved' > "${rc}"
  cp "${rc}" "${rc}.original"

  env HOME="${test_home}" XDG_CONFIG_HOME="${config}" SHELL="$(command -v bash)" \
    "${BIN}" completion install bash >/dev/null
  env HOME="${test_home}" XDG_CONFIG_HOME="${config}" SHELL="$(command -v bash)" \
    "${BIN}" completion install bash >/dev/null
  assert_one_block "${rc}"
  env PATH="${TEST_PATH}" HOME="${test_home}" XDG_CONFIG_HOME="${config}" \
    bash --noprofile --norc -c 'source "$HOME/.bashrc"; complete -p shinyhub' >/dev/null \
    || fail "bash could not load the installed completion"
  env HOME="${test_home}" XDG_CONFIG_HOME="${config}" SHELL="$(command -v bash)" \
    "${BIN}" completion uninstall bash >/dev/null
  cmp -s "${rc}" "${rc}.original" || fail "bash startup file was not restored byte-for-byte"
  assert_removed "${script}"
  echo "    bash: install, reload, reinstall, and uninstall passed"
}

test_zsh() {
  local test_home="${WORK}/zsh-home"
  local config="${test_home}/config"
  local rc="${test_home}/.zshrc"
  local script="${config}/shinyhub/completions/shinyhub.zsh"
  mkdir -p "${test_home}"
  printf 'export SHINYHUB_ZSH_SENTINEL=preserved' > "${rc}"
  cp "${rc}" "${rc}.original"

  env HOME="${test_home}" XDG_CONFIG_HOME="${config}" ZDOTDIR="${test_home}" SHELL="$(command -v zsh)" \
    "${BIN}" completion install zsh >/dev/null
  env HOME="${test_home}" XDG_CONFIG_HOME="${config}" ZDOTDIR="${test_home}" SHELL="$(command -v zsh)" \
    "${BIN}" completion install zsh >/dev/null
  assert_one_block "${rc}"
  env PATH="${TEST_PATH}" HOME="${test_home}" XDG_CONFIG_HOME="${config}" ZDOTDIR="${test_home}" \
    zsh -dfc 'source "$ZDOTDIR/.zshrc"; (( $+functions[_shinyhub] ))' \
    || fail "zsh could not load the installed completion"
  env HOME="${test_home}" XDG_CONFIG_HOME="${config}" ZDOTDIR="${test_home}" SHELL="$(command -v zsh)" \
    "${BIN}" completion uninstall zsh >/dev/null
  cmp -s "${rc}" "${rc}.original" || fail "zsh startup file was not restored byte-for-byte"
  assert_removed "${script}"
  echo "    zsh: install, reload, reinstall, and uninstall passed"
}

test_fish() {
  local test_home="${WORK}/fish-home"
  local config="${test_home}/config"
  local script="${config}/fish/completions/shinyhub.fish"
  mkdir -p "${test_home}"

  env HOME="${test_home}" XDG_CONFIG_HOME="${config}" SHELL="$(command -v fish)" \
    "${BIN}" completion install fish >/dev/null
  env HOME="${test_home}" XDG_CONFIG_HOME="${config}" SHELL="$(command -v fish)" \
    "${BIN}" completion install fish >/dev/null
  env PATH="${TEST_PATH}" HOME="${test_home}" XDG_CONFIG_HOME="${config}" \
    fish --no-config -c 'source "$XDG_CONFIG_HOME/fish/completions/shinyhub.fish"; complete -C "shinyhub co"' \
    | grep -Eq 'completion|connect' || fail "fish could not execute the installed completion"
  env HOME="${test_home}" XDG_CONFIG_HOME="${config}" SHELL="$(command -v fish)" \
    "${BIN}" completion uninstall fish >/dev/null
  assert_removed "${script}"
  echo "    fish: install, execute, reinstall, and uninstall passed"
}

test_powershell() {
  local test_home="${WORK}/powershell-home"
  local config="${test_home}/config"
  local profile="${config}/powershell/Microsoft.PowerShell_profile.ps1"
  local script="${config}/powershell/shinyhub-completion.ps1"
  mkdir -p "$(dirname "${profile}")"
  printf '$Global:ShinyHubCompletionSentinel = "preserved"' > "${profile}"
  cp "${profile}" "${profile}.original"

  env HOME="${test_home}" XDG_CONFIG_HOME="${config}" SHELL="${POWERSHELL_BIN}" \
    "${BIN}" completion install powershell >/dev/null
  env HOME="${test_home}" XDG_CONFIG_HOME="${config}" SHELL="${POWERSHELL_BIN}" \
    "${BIN}" completion install powershell >/dev/null
  assert_one_block "${profile}"
  env PATH="${TEST_PATH}" HOME="${test_home}" XDG_CONFIG_HOME="${config}" \
    "${POWERSHELL_BIN}" -NoLogo -NoProfile -NonInteractive -Command '
      $profilePath = Join-Path $env:XDG_CONFIG_HOME "powershell/Microsoft.PowerShell_profile.ps1"
      . $profilePath
      function Get-PSReadLineKeyHandler { [PSCustomObject]@{ Key = "Tab"; Function = "MenuComplete" } }
      $tokens = $null
      $errors = $null
      $ast = [System.Management.Automation.Language.Parser]::ParseInput("shinyhub co", [ref]$tokens, [ref]$errors)
      $commandAst = $ast.EndBlock.Statements[0].PipelineElements[0]
      $matches = & ${__shinyhubCompleterBlock} "co" $commandAst 11
      $texts = $matches | ForEach-Object { $_.CompletionText.Trim() }
      if (($texts -notcontains "completion") -and ($texts -notcontains "connect")) {
        Write-Error ("completion results: " + ($texts -join ", "))
        exit 1
      }
    ' || fail "PowerShell could not execute the installed completion"
  env HOME="${test_home}" XDG_CONFIG_HOME="${config}" SHELL="${POWERSHELL_BIN}" \
    "${BIN}" completion uninstall powershell >/dev/null
  cmp -s "${profile}" "${profile}.original" || fail "PowerShell profile was not restored byte-for-byte"
  assert_removed "${script}"
  echo "    PowerShell: install, execute, reinstall, and uninstall passed"
}

echo "==> exercising installed completion in real shells"
for shell in ${COMPLETION_SHELLS}; do
  case "${shell}" in
    bash) test_bash ;;
    zsh) test_zsh ;;
    fish) test_fish ;;
    powershell) test_powershell ;;
  esac
done

echo "SHELL COMPLETION E2E PASS (${COMPLETION_SHELLS})"
