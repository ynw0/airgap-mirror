# Linux Mirror Agent 与 Apache2 部署

本文只部署控制面。现有 APT、PyPI、Maven 和 npm tarball 目录继续由 Apache2 直接提供，不搬库、不改 URL、不让 Agent 代理大文件。

## 1. 构建 Linux 二进制

在与目标 Linux 架构一致的构建环境执行：

```bash
go build -o dist/mirror-agent ./cmd/mirror-agent
go build -o dist/mirrorctl ./cmd/mirrorctl
```

Mirror Agent 当前运行参数：

```text
-listen   127.0.0.1:8787
-db       /var/lib/airgap-mirror/server.db
-staging  /var/lib/airgap-mirror/transfer-staging
-exports  /var/lib/airgap-mirror/state-exports
```

Bearer Token 优先通过 `AIRGAP_MIRROR_TOKEN` 提供。服务在没有 Token 时拒绝启动。

## 2. 安装 Agent

```bash
sudo ./scripts/install-agent.sh ./dist/mirror-agent ./dist/mirrorctl
```

安装器会：

- 创建系统用户/组 `airgap-mirror`；
- 安装 `/usr/local/bin/mirror-agent`，可选安装 `mirrorctl`；
- 创建 `/etc/airgap-mirror/agent.env`；首次安装使用 `/dev/urandom` 生成 32 字节随机 Token；
- 创建 `/var/lib/airgap-mirror`、transfer staging 和 State Capsule export 目录；
- 安装并 enable systemd unit；
- **不会**启动服务；
- **不会**修改 Apache；
- **不会**移动或修改已有仓库目录。

启动前必须让 `airgap-mirror` 对每个 Source 的 `rootPath` 有写权限。Apache 只需要读权限。若已有统一仓库组，可将 `airgap-mirror` 与 Apache 用户加入该组，并让仓库目录使用 setgid；不要为了省事把仓库改成 `0777`。

示例：

```bash
sudo groupadd -f mirror
sudo usermod -aG mirror airgap-mirror
sudo usermod -aG mirror www-data
sudo chgrp -R mirror /srv/mirror
sudo find /srv/mirror -type d -exec chmod 2775 {} +
sudo find /srv/mirror -type f -exec chmod 0664 {} +
```

上面的 `/srv/mirror` 只是示例。已有仓库路径无需迁移。

确认环境文件：

```bash
sudo stat /etc/airgap-mirror/agent.env
sudo systemctl start airgap-mirror-agent
sudo systemctl status airgap-mirror-agent --no-pager
```

本机验证：

```bash
curl http://127.0.0.1:8787/api/v1/health
sudo -u airgap-mirror env "$(cat /etc/airgap-mirror/agent.env)" \
  mirrorctl -url http://127.0.0.1:8787 sources
```

## 3. Apache2 集成

启用模块：

```bash
sudo a2enmod proxy proxy_http alias
```

将 `deploy/apache/airgap-mirror.conf` 的内容合并到现有内部仓库 VirtualHost。推荐对外入口：

```text
https://repo.internal/mirror-control/   -> Mirror Agent 控制面
https://repo.internal/npm/              -> npm packument gateway
```

桌面客户端中的 Agent URL 填：

```text
https://repo.internal/mirror-control
```

客户端会在此基础上追加 `/api/v1/...`。

`/npm/` 是 npm metadata gateway，不使用 Agent Bearer Token；npm CLI 无法携带控制面 Token。Gateway 只读取已经发布且 Catalog 登记的 packument。

npm tarball 必须继续由 Apache 静态提供。例如：

```text
https://repo.internal/npm-files/
```

因此 npm Source 的 `publicUrl` 应指向 `npm-files` 静态根，**不能**指向 `/npm/`。

若 npm 客户端会把 scoped package 的 `/` 编码成 `%2F`，VirtualHost 需要：

```apache
AllowEncodedSlashes NoDecode
```

修改后先检查 Apache 配置再 reload：

```bash
sudo apachectl configtest
sudo systemctl reload apache2
```

## 4. Source 创建

控制 API 使用 Bearer Token：

```bash
TOKEN="$(sudo sed -n 's/^AIRGAP_MIRROR_TOKEN=//p' /etc/airgap-mirror/agent.env)"
BASE=https://repo.internal/mirror-control
```

### APT

一个 Source 对应一个完整 suite publication set。`releaseMode` 只能是 `inrelease` 或 `detached`。

```bash
curl -fsS -X POST "$BASE/api/v1/sources" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  --data '{
    "name":"Debian bookworm",
    "type":"apt",
    "provider":"apt-generic",
    "upstreamUrl":"https://deb.debian.org/debian",
    "rootPath":"/srv/mirror/apt/debian",
    "publicUrl":"https://repo.internal/apt/debian",
    "enabled":true,
    "config":{"suite":"bookworm","releaseMode":"inrelease"}
  }'
```

### PyPI

`upstreamUrl` 是 PEP 691 Simple API 根。仓库布局中生成 `simple/<project>/index.html`，distribution 文件保持 `packages/...` 原生相对路径。

```bash
curl -fsS -X POST "$BASE/api/v1/sources" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  --data '{
    "name":"PyPI",
    "type":"pypi",
    "provider":"pypi",
    "upstreamUrl":"https://pypi.org/simple",
    "rootPath":"/srv/mirror/pypi",
    "publicUrl":"https://repo.internal/pypi/simple",
    "enabled":true,
    "config":{}
  }'
```

pip 使用示例：

```bash
pip install --index-url https://repo.internal/pypi/simple/ <package>
```

### npm

`upstreamUrl` 是 packument registry，`replicationUrl` 是变化流。`publicUrl` 是 **静态 tarball 根**。

```bash
curl -fsS -X POST "$BASE/api/v1/sources" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  --data '{
    "name":"npmjs",
    "type":"npm",
    "provider":"npmjs",
    "upstreamUrl":"https://registry.npmjs.org",
    "rootPath":"/srv/mirror/npm",
    "publicUrl":"https://repo.internal/npm-files",
    "enabled":true,
    "config":{"replicationUrl":"https://replicate.npmjs.com/registry","changePageSize":10000,"allDocsPageSize":10000}
  }'
```

npm 客户端 registry 使用 Gateway URL，并包含 Source ID：

```text
https://repo.internal/npm/<source-id>/
```

packument 中的 `dist.tarball` 会被 Adapter 重写到 `publicUrl/<原生 tarball logical path>`，因此下载流量不会经过 Agent。

### Maven Generic

Generic Provider **不爬 HTML 目录**。上游必须提供可枚举 NDJSON manifest：

```bash
curl -fsS -X POST "$BASE/api/v1/sources" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  --data '{
    "name":"Internal upstream Maven",
    "type":"maven",
    "provider":"maven-generic",
    "upstreamUrl":"https://upstream.example/repository/releases",
    "rootPath":"/srv/mirror/maven/internal",
    "publicUrl":"https://repo.internal/maven/internal",
    "enabled":true,
    "config":{"indexUrl":"https://upstream.example/mirror-index.ndjson"}
  }'
```

### Maven Central dependency-set

Central Provider 只接受 exact release coordinates；不支持全库镜像、SNAPSHOT、版本范围、`LATEST` 或 `RELEASE`。

```bash
curl -fsS -X POST "$BASE/api/v1/sources" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  --data '{
    "name":"Maven Central approved set",
    "type":"maven",
    "provider":"maven-central",
    "upstreamUrl":"https://repo.maven.apache.org/maven2",
    "rootPath":"/srv/mirror/maven/central",
    "publicUrl":"https://repo.internal/maven/central",
    "enabled":true,
    "config":{"mode":"dependency-set","coordinates":[
      {"groupId":"org.slf4j","artifactId":"slf4j-api","version":"2.0.17","packaging":"jar"}
    ]}
  }'
```

## 5. 运维边界

- Apache 静态仓库目录是真实数据面；不要把制品导入 SQLite。
- 正常同步不会扫描整个几十 TB 仓库。
- Pack upload staging 只保存正在导入的 Pack，commit 后会释放。
- 同 logical path 不同 hash 是硬冲突，Agent 不覆盖。
- npm unpublish 等删除先撤销 metadata 引用，旧 tarball 等后续显式 GC 回收。
- State Capsule 可以带出隔离网，但不要把 Agent Bearer Token 写进 Capsule 或同步介质。
- `/mirror-control/` 应只暴露在可信内网；Bearer Token 是控制面身份凭据，不替代网络分区、TLS 和主机访问控制。
