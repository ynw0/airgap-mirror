#!/usr/bin/env bash
set -euo pipefail

if [[ ${EUID} -ne 0 ]]; then
  echo "error: run as root" >&2
  exit 1
fi

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# 与 release tarball 的 bin/ 布局一致；解压后无参即可安装。
AGENT_BIN="${1:-${REPO_ROOT}/bin/mirror-agent}"
CTL_BIN="${2:-${REPO_ROOT}/bin/mirrorctl}"
SERVICE_USER=airgap-mirror
SERVICE_GROUP=airgap-mirror
ETC_DIR=/etc/airgap-mirror
STATE_DIR=/var/lib/airgap-mirror

if [[ ! -f "${AGENT_BIN}" ]]; then
  echo "error: mirror-agent binary not found: ${AGENT_BIN}" >&2
  echo "build it first: go build -o bin/mirror-agent ./cmd/mirror-agent" >&2
  exit 1
fi

# Do not overwrite an installed service while it may be writing repository data.
if systemctl is-active --quiet airgap-mirror-agent.service; then
  echo "请先确认导入和清理任务已结束，暂停可恢复的清点任务，然后停止 Agent 并备份状态目录。" >&2
  echo "sudo systemctl stop airgap-mirror-agent" >&2
  echo "停止并备份后重新运行本安装脚本；详见 docs/DEPLOYMENT.md 的升级说明。" >&2
  exit 1
fi
if [[ ! -f "${REPO_ROOT}/deploy/systemd/airgap-mirror-agent.service" ]]; then
  echo "error: systemd service file not found" >&2
  exit 1
fi

# Check the binaries on this host before overwriting an installed Agent.
for binary in "${AGENT_BIN}" "${CTL_BIN}"; do
  [[ -f "${binary}" ]] || continue
  if ! output="$("${binary}" -h 2>&1)"; then
    printf '无法运行新版程序：%s\n%s\n' "${binary}" "${output}" >&2
    echo "尚未替换现有程序，请检查服务器架构及运行环境。" >&2
    exit 1
  fi
done

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
  "${STATE_DIR}/transfer-staging" "${STATE_DIR}/state-exports" "${STATE_DIR}/maintenance"

ENV_FILE="${ETC_DIR}/agent.env"
if [[ ! -e "${ENV_FILE}" ]]; then
  token="$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')"
  if [[ ${#token} -ne 64 ]]; then
    echo "error: failed to generate Agent token" >&2
    exit 1
  fi
  umask 0027
  printf 'AIRGAP_MIRROR_TOKEN=%s\nAIRGAP_MIRROR_LISTEN=127.0.0.1:8787\n' "${token}" > "${ENV_FILE}"
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
echo "  3. Configure the Agent listen address with scripts/configure-agent.sh."
echo "  4. Confirm ${ENV_FILE} and the repository source definitions."
echo
echo "Then run:"
echo "  systemctl start airgap-mirror-agent"
echo "  systemctl status airgap-mirror-agent --no-pager"
echo
echo "The installer intentionally does not modify Apache or existing repository trees."
