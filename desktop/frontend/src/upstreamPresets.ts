import type { Source } from './types'

const presets: Record<string, string[][]> = {
  "apt": [
    [
      "Debian 官方",
      "https://deb.debian.org/debian",
      "bookworm"
    ],
    [
      "Debian 安全更新",
      "https://deb.debian.org/debian-security",
      "bookworm-security"
    ],
    [
      "Ubuntu 官方",
      "https://archive.ubuntu.com/ubuntu",
      "noble"
    ],
    [
      "Ubuntu 安全更新",
      "https://security.ubuntu.com/ubuntu",
      "noble-security"
    ],
    [
      "清华大学 · Debian",
      "https://mirrors.tuna.tsinghua.edu.cn/debian",
      "bookworm"
    ],
    [
      "清华大学 · Ubuntu",
      "https://mirrors.tuna.tsinghua.edu.cn/ubuntu",
      "noble"
    ],
    [
      "中科大 · Debian",
      "https://mirrors.ustc.edu.cn/debian",
      "bookworm"
    ],
    [
      "中科大 · Ubuntu",
      "https://mirrors.ustc.edu.cn/ubuntu",
      "noble"
    ],
    [
      "阿里云 · Debian",
      "https://mirrors.aliyun.com/debian",
      "bookworm"
    ],
    [
      "阿里云 · Ubuntu",
      "https://mirrors.aliyun.com/ubuntu",
      "noble"
    ],
    [
      "华为云 · Debian",
      "https://mirrors.huaweicloud.com/debian",
      "bookworm"
    ],
    [
      "华为云 · Ubuntu",
      "https://mirrors.huaweicloud.com/ubuntu",
      "noble"
    ]
  ],
  "pypi": [
    [
      "PyPI 官方",
      "https://pypi.org/simple"
    ],
    [
      "清华大学",
      "https://mirrors.tuna.tsinghua.edu.cn/pypi/web/simple"
    ],
    [
      "中科大",
      "https://mirrors.ustc.edu.cn/pypi/simple"
    ],
    [
      "阿里云",
      "https://mirrors.aliyun.com/pypi/simple"
    ],
    [
      "华为云",
      "https://mirrors.huaweicloud.com/repository/pypi/simple"
    ]
  ],
  "npm": [
    [
      "npm 官方",
      "https://registry.npmjs.org"
    ],
    [
      "npmmirror",
      "https://registry.npmmirror.com"
    ],
    [
      "华为云",
      "https://mirrors.huaweicloud.com/repository/npm"
    ]
  ],
  "maven-central": [
    [
      "Maven Central 官方",
      "https://repo.maven.apache.org/maven2"
    ],
    [
      "Maven Central 备用官方地址",
      "https://repo1.maven.org/maven2"
    ],
    [
      "阿里云 Central 镜像",
      "https://maven.aliyun.com/repository/central"
    ],
    [
      "中科大代理",
      "https://maven.proxy.ustclug.org/maven2"
    ]
  ],
  "maven-generic": []
}

export function upstreamChoices(source: Source): string[][] {
  if (source.type !== 'apt') return presets[source.provider] ?? presets[source.type] ?? []
  const config = sourceConfig(source)
  const suite = String((config as {suite?: string} | null)?.suite ?? '').toLowerCase()
  const ubuntu = /^(focal|jammy|noble|resolute)(-|$)/.test(suite)
  const security = suite.endsWith('-security')
  const known = ubuntu || /^(bullseye|bookworm|trixie|forky|sid|stable|oldstable|oldoldstable|testing|unstable)(-|$)/.test(suite)
  if (!known) return []
  const rows = presets.apt.filter(p => /^(focal|jammy|noble|resolute)(-|$)/.test(p[2]) === ubuntu)
  if (ubuntu) return security ? [rows[1], ...rows.filter(p => !p[2].endsWith('-security'))] : rows.filter(p => !p[2].endsWith('-security'))
  if (security) return [rows[1], ...rows.slice(2).map(p => [p[0], p[1] + '-security', p[2]])]
  return rows.filter(p => !p[2].endsWith('-security'))
}

export function sourceConfig(source: Source): Record<string, unknown> {
  try {
    const value = typeof source.config === 'string' ? JSON.parse(source.config) : source.config
    return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {}
  } catch { return {} }
}
