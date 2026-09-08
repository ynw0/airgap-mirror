#!/usr/bin/env bash
set -euo pipefail

if [[ ${EUID} -ne 0 ]]; then
  echo "error: run as root" >&2
  exit 1
fi

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
AGENT_BIN="${1:-${REPO_ROOT}/dist/mirror-agent}"
CTL_BIN="${2:-${REPO_ROOT}/dist/mirrorctl}"
SERVICE_USER=airgap-mirror
SERVICE_GROUP=airgap-mirror
ETC_DIR=/etc/airgap-mirror
STATE_DIR=/var/lib/airgap-mirror

if [[ ! -f "${AGENT_BIN}" ]]; then
  echo "error: mirror-agent binary not found: ${AGENT_BIN}" >&2
  echo "build it first: go build -o dist/mirror-agent ./cmd/mirror-agent" >&2
  exit 1
fi

if ! getent group "${SERVICE_GROUP}" >/dev/null; then
  groupadd --system "${SERVICE_GROUP}"
fi
if ! id -u "${SERVICE_USER}" >/dev/null 2>&1; then
  useradd --system --gid "${SERVICE_GROUP}" --home-dir "${STATE_DIR}" --no-create-home --shell /usr/sbin/nologin "${SERVICE_USER}"
fi

install -D -m 0755 "${AGENT_BIN}" /usr/local/bin/mirror-agent
if [[ -f "${CTL_BIN}" ]]; then
  install -D -m 0755 "${CTL_BIN}" /usr/local/bin/mirrorctl
fi

install -d -m 0750 -o root -g "${SERVICE_GROUP}" "${ETC_DIR}"
install -d -m 0750 -o "${SERVICE_USER}" -g "${SERVICE_GROUP}" "${STATE_DIR}"
install -d -m 0750 -o "${SERVICE_USER}" -g "${SERVICE_GROUP}" \
  "${STATE_DIR}/transfer-staging" "${STATE_DIR}/state-exports"

ENV_FILE="${ETC_DIR}/agent.env"
if [[ ! -e "${ENV_FILE}" ]]; then
  token="$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')"
  if [[ ${#token} -ne 64 ]]; then
    echo "error: failed to generate Agent token" >&2
    exit 1
  fi
  umask 0027
  printf 'AIRGAP_MIRROR_TOKEN=%s\n' "${token}" > "${ENV_FILE}"
  chown root:"${SERVICE_GROUP}" "${ENV_FILE}"
  chmod 0640 "${ENV_FILE}"
  echo "generated ${ENV_FILE}"
else
  chown root:"${SERVICE_GROUP}" "${ENV_FILE}"
  chmod 0640 "${ENV_FILE}"
fi

install -D -m 0644 "${REPO_ROOT}/deploy/systemd/airgap-mirror-agent.service" /etc/systemd/system/airgap-mirror-agent.service
systemctl daemon-reload
systemctl enable airgap-mirror-agent.service

echo
echo "Agent files installed."
echo "Before starting the service:"
echo "  1. Grant user '${SERVICE_USER}' write permission to every configured repository RootPath."
echo "  2. Adapt deploy/apache/airgap-mirror.conf to the existing Apache VirtualHost."
echo "  3. Confirm ${ENV_FILE} and the repository source definitions."
echo
echo "Then run:"
echo "  systemctl start airgap-mirror-agent"
echo "  systemctl status airgap-mirror-agent --no-pager"
echo
echo "The installer intentionally does not modify Apache or existing repository trees."
