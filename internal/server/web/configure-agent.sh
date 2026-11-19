#!/usr/bin/env bash
# 仅配置已安装的 Agent，不修改 Apache 和仓库文件。
set -euo pipefail
if [[ $EUID -ne 0 ]]; then
  echo "请使用 sudo bash configure-agent.sh --listen 192.168.21.4:8787" >&2
  exit 1
fi
env_file=/etc/airgap-mirror/agent.env
if [[ ! -f $env_file ]] || ! command -v mirror-agent >/dev/null 2>&1; then
  echo "请先运行发布包中的 sudo bash scripts/install-agent.sh" >&2
  exit 1
fi
if [[ $# -eq 2 && $1 == "--listen" ]]; then
  listen="$2"
elif [[ $# -eq 0 ]]; then
  current="$(sed -n 's/^AIRGAP_MIRROR_LISTEN=//p' "$env_file" | tail -n 1)"
  if [[ -z $current ]]; then current="127.0.0.1:8787"; fi
  read -r -p "Agent 监听地址（例如 192.168.21.4:8787）[$current]：" listen
  if [[ -z $listen ]]; then listen="$current"; fi
else
  echo "用法：sudo bash configure-agent.sh [--listen 192.168.21.4:8787]" >&2
  exit 2
fi
if [[ ! $listen =~ ^[A-Za-z0-9.-]+:[0-9]{1,5}$ ]]; then
  echo "请输入 IP:端口（例如 192.168.21.4:8787）。" >&2
  exit 2
fi
port="$(printf '%s' "$listen" | awk -F: '{print $NF}')"
if (( 10#$port < 1 || 10#$port > 65535 )); then
  echo "端口必须在 1 至 65535 之间。" >&2
  exit 2
fi
tmp="$(mktemp /etc/airgap-mirror/.agent.env.XXXXXX)"
trap 'rm -f "$tmp"' EXIT
grep -v '^AIRGAP_MIRROR_LISTEN=' "$env_file" > "$tmp" || true
printf 'AIRGAP_MIRROR_LISTEN=%s\n' "$listen" >> "$tmp"
install -m 0640 -o root -g airgap-mirror "$tmp" "$env_file"
systemctl daemon-reload
systemctl restart airgap-mirror-agent
if ! systemctl is-active --quiet airgap-mirror-agent; then
  echo "服务未能启动，请检查：journalctl -u airgap-mirror-agent -n 50 --no-pager" >&2
  exit 1
fi
echo "已保存配置，管理页面：http://$listen/"
echo "连接令牌由服务器管理员从 $env_file 安全提供。"
echo "请确认 IP 属于本机，并根据实际网络调整防火墙。HTTP 不加密，请仅在可信局域网使用。"
