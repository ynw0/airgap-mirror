#!/usr/bin/env bash
# 为内网 APT 生成独立签名密钥，不自动在客户端安装信任。
set -euo pipefail
if [[ $EUID -ne 0 ]]; then echo "请用 sudo 运行此脚本。" >&2; exit 1; fi
if ! command -v gpg >/dev/null 2>&1; then echo "没有 gpg，请先安装 gnupg。" >&2; exit 1; fi
if ! id airgap-mirror >/dev/null 2>&1 || [[ ! -f /etc/airgap-mirror/agent.env ]]; then
  echo "请先安装 Mirror Agent。" >&2; exit 1
fi
home=/var/lib/airgap-mirror/gnupg
install -d -m 0700 -o airgap-mirror -g airgap-mirror "$home"
fingerprint="$(runuser -u airgap-mirror -- env GNUPGHOME="$home" gpg --batch --with-colons --list-secret-keys 2>/dev/null | awk -F: '$1=="fpr" {print $10; exit}')"
if [[ -z $fingerprint ]]; then
  read -r -p "没有专用签名密钥。现在创建新的内网密钥？[y/N] " answer
  if [[ $answer != y && $answer != Y ]]; then echo "已取消。"; exit 0; fi
  runuser -u airgap-mirror -- env GNUPGHOME="$home" gpg --batch --pinentry-mode loopback --passphrase '' --quick-generate-key \
    "AirGap Mirror Local Repository <airgap-mirror@local.invalid>" default default 2y
  fingerprint="$(runuser -u airgap-mirror -- env GNUPGHOME="$home" gpg --batch --with-colons --list-secret-keys | awk -F: '$1=="fpr" {print $10; exit}')"
fi
if [[ ! $fingerprint =~ ^[A-Fa-f0-9]{40,64}$ ]]; then echo "无法读取密钥指纹。" >&2; exit 1; fi
env_file=/etc/airgap-mirror/agent.env
tmp="$(mktemp /etc/airgap-mirror/.agent.env.XXXXXX)"
trap 'rm -f "$tmp"' EXIT
grep -Ev '^AIRGAP_MIRROR_(APT_SIGNING_KEY|GNUPGHOME)=' "$env_file" > "$tmp" || true
printf 'AIRGAP_MIRROR_GNUPGHOME=%s\nAIRGAP_MIRROR_APT_SIGNING_KEY=%s\n' "$home" "$fingerprint" >> "$tmp"
install -m 0640 -o root -g airgap-mirror "$tmp" "$env_file"
systemctl restart airgap-mirror-agent
echo "已设置专用签名密钥：$fingerprint"
echo "先在管理页面完成文件清点，再点击重新签名。签名后给内网 APT 客户端安装下载的公钥。"
echo "请保护并备份 $home，不要将私钥复制到公网电脑。"
