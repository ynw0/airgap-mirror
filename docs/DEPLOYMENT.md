# Linux Mirror Agent 与 Apache2 部署

本文只部署控制面。现有 APT、PyPI、Maven 和 npm tarball 目录继续由 Apache2 直接提供，不搬库、不改 URL、不让 Agent 代理大文件。

## 1. 构建 Linux 二进制

在与目标 Linux 架构一致的构建环境执行：

```bash
go build -o bin/mirror-agent ./cmd/mirror-agent
go build -o bin/mirrorctl ./cmd/mirrorctl
```

Mirror Agent 当前运行参数：

```text
-listen       127.0.0.1:8787
-db           /var/lib/airgap-mirror/server.db
-staging      /var/lib/airgap-mirror/transfer-staging
-exports      /var/lib/airgap-mirror/state-exports
-maintenance  /var/lib/airgap-mirror/maintenance
```

Bearer Token 优先通过 `AIRGAP_MIRROR_TOKEN` 提供。服务在没有 Token 时拒绝启动。

## 2. 安装 Agent

```bash
sudo ./scripts/install-agent.sh
```

安装器默认读取包内 `bin/mirror-agent` 与 `bin/mirrorctl`。`v*` release 的 `airgap-mirror-linux-amd64.tar.gz` 解压后直接执行即可。若二进制在其他路径，把两个文件作为参数传入。

安装器会：

- 创建系统用户/组 `airgap-mirror`；
- 安装 `/usr/local/bin/mirror-agent`，可选安装 `mirrorctl`；
- 创建 `/etc/airgap-mirror/agent.env`；首次安装使用 `/dev/urandom` 生成 32 字节随机 Token；
- 创建 `/var/lib/airgap-mirror`、transfer staging、State Capsule export、maintenance workspace；
- 安装并 enable systemd unit；
- **不会**启动服务；
- **不会**修改 Apache；
- **不会**移动或修改已有仓库目录。

启动前必须让 `airgap-mirror` 对每个 Source 的 `rootPath` 有写权限。Apache 只需要读权限。若已有统一仓库组，可将 `airgap-mirror` 与 Apache 用户加入该组，并让仓库目录使用 setgid；不要将仓库改成 `0777`。

示例：

```bash
sudo groupadd -f mirror
sudo usermod -aG mirror airgap-mirror
sudo usermod -aG mirror www-data
sudo chgrp -R mirror /srv/mirror
sudo find /srv/mirror -type d -exec chmod 2775 {} +
sudo find /srv/mirror -type f -exec chmod 0664 {} +
```

已有仓库路径无需迁移。

确认环境并启动：

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

### 已安装 Agent 的升级

先确认没有正在提交文件包、发布元数据或执行实际文件清理的任务。暂停可恢复的 PyPI 清点或检查任务，待其状态变为已暂停后，再停止服务。

从新版发布包解压目录执行以下命令。备份目录应位于有足够空间的存储设备上，且不得位于待备份目录或软件仓库内。默认状态目录可能包含很大的清点数据库和未完成的上传文件，请先检查空间。若使用自定义路径，备份实际使用的配置、数据库、暂存目录和维护目录。

```bash
sudo systemctl stop airgap-mirror-agent
backup="/srv/backup/airgap-mirror-$(date +%Y%m%d-%H%M%S)"
sudo install -d -m 0700 "$backup"
sudo cp -a /etc/airgap-mirror /var/lib/airgap-mirror "$backup/"
sudo bash scripts/install-agent.sh
sudo systemctl start airgap-mirror-agent
sudo systemctl status airgap-mirror-agent --no-pager
curl -fsS http://127.0.0.1:8787/api/v1/health
```

以上健康检查使用默认监听地址；若已自定义监听地址，请使用实际地址。任何一步失败时先排查，不要继续后面的命令。原 Token、监听配置和仓库路径保持不变，无需重新配置 Apache。启动后检查软件源、Catalog 版本、未完成任务，再继续原有工作。

此备份只包含控制面状态，不包含软件仓库本体；不等同于仓库数据备份。升级后不要直接换回旧二进制文件运行新版数据库。若需要回退，先停止服务，核对升级后仓库是否发生变化，再恢复与旧版本匹配的备份及旧程序。

## 3. Apache2 集成

启用模块：

```bash
sudo a2enmod proxy proxy_http alias
```

将 `deploy/apache/airgap-mirror.conf` 合并到现有内部仓库 VirtualHost。推荐入口：

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

npm tarball 必须继续由 Apache 静态提供，例如：

```text
https://repo.internal/npm-files/
```

因此 npm Source 的 `publicUrl` 应指向 `npm-files` 静态根，**不能**指向 `/npm/`。

若 npm 客户端会把 scoped package 的 `/` 编码成 `%2F`，VirtualHost 需要：

```apache
AllowEncodedSlashes NoDecode
```

修改后检查配置再 reload：

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

也可用桌面客户端「源状态」页或 `mirrorctl` 提交同一份 JSON：

```bash
mirrorctl -url "$BASE" -token "$TOKEN" sources-create ./source.json
mirrorctl -url "$BASE" -token "$TOKEN" sources-update <source-id> ./source.json
```

### APT

一个 Source 对应一个 suite publication set。`releaseMode` 只能是 `inrelease` 或 `detached`。

`pool/` 在 Debian 仓库中可能跨多个 suite 共用，因此 physical GC **默认不管理 pool**。只有当该 Source 的整个 `pool/` 确认不存在其他 suite/Source/工具引用时，才可设置 `gcOwnsPool=true`。

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
    "config":{"suite":"bookworm","releaseMode":"inrelease","gcOwnsPool":false}
  }'
```

### PyPI

`upstreamUrl` 是 PEP 691 Simple API 根。仓库布局生成 `simple/<project>/index.html`，distribution 文件保持 `packages/...` 原生相对路径。

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

pip 使用：

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

packument 中的 `dist.tarball` 会被 Adapter 重写到 `publicUrl/<原生 tarball logical path>`，下载流量不会经过 Agent。

### Maven Generic

Generic Provider **不爬 HTML 目录**。上游必须提供可枚举 NDJSON manifest。若需要对一个已有 filesystem repository 执行显式 Inventory/GC，配置 `inventoryMode=filesystem`；这表示维护任务允许遍历该 Source 的独占 root，不改变正常增量同步方式。

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
    "config":{"indexUrl":"https://upstream.example/mirror-index.ndjson","inventoryMode":"filesystem"}
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

## 5. Inventory 与 GC

Maintenance 是显式控制面操作，不会在正常同步中自动扫描几十 TB 仓库。

启动 Inventory：

```bash
mirrorctl -url "$BASE" -token "$TOKEN" inventory <source-id>
```

启动 GC dry-run：

```bash
mirrorctl -url "$BASE" -token "$TOKEN" gc <source-id>
```

物理删除必须显式传入 `execute`：

```bash
mirrorctl -url "$BASE" -token "$TOKEN" gc <source-id> execute
```

取消未完成的 Epoch，清掉 ActiveEpoch，使 Inventory、GC 或下一轮导入可以开始：

```bash
mirrorctl -url "$BASE" -token "$TOKEN" epochs-cancel <epoch-id>
```

已经完成或进入文件安装阶段的 Epoch 不能直接取消；后一种情况需要继续完成导入。桌面客户端「源状态」页提供相同操作。

查看任务：

```bash
mirrorctl -url "$BASE" -token "$TOKEN" maintenance <source-id>
mirrorctl -url "$BASE" -token "$TOKEN" maintenance-get <job-id>
```

查看完成的 GC 候选：

```bash
curl -fsS "$BASE/api/v1/maintenance/<job-id>/candidates?limit=100" \
  -H "Authorization: Bearer $TOKEN"
```

`gc … execute` 会创建一个**新的 GC Job 并重新扫描**，不会直接删除上一次 dry-run 的旧候选。删除前会再次检查 Catalog 引用、仓库根目录身份、文件类型、大小和最后修改时间。

安全边界：

- Active Epoch 与 Active Maintenance Job 互斥；
- 实际文件清理要求仓库根目录不与其他软件源相同、嵌套或解析后重叠；独立的其他仓库暂未挂载不影响清理。执行期间仅禁止新增或修改与当前清理目录重叠的仓库目录；预览只根据当前软件源的 Catalog 列出候选，不代表允许实际删除共享目录中的文件；
- repository tree 出现 symlink 时 physical GC 拒绝继续；
- physical delete 仅在 Linux 实现；
- APT `pool/` 只有 `gcOwnsPool=true` 才进入删除范围。

## 6. 运维边界

- Apache 静态仓库目录是真实数据面；不要把制品导入 SQLite。
- 正常同步不会扫描整个几十 TB 仓库。
- Pack upload staging 只保存正在导入的 Pack，commit 后释放。
- Maintenance workspace 只保存隔离 rebuild Catalog 与 GC candidate DB，不保存仓库本体副本。
- 同 logical path 不同 hash 是硬冲突，Agent 不覆盖。
- npm unpublish 等删除先撤销 metadata 引用，旧 tarball 通过显式 GC 回收。
- State Capsule 可以带出隔离网，但不要把 Agent Bearer Token 写进 Capsule 或同步介质。
- `/mirror-control/` 应只暴露在可信内网；Bearer Token 不替代网络分区、TLS 和主机访问控制。

## 7. Build 与 Release

`main`/Pull Request 由 `.github/workflows/ci.yml` 执行 Go 格式检查、自动化测试、竞态检测、部署脚本语法检查和 Linux/Windows 构建。推送 `v*` tag 后 `.github/workflows/release.yml` 会构建 Linux/Windows amd64 bundle、生成 SHA-256 并创建 GitHub Release。
