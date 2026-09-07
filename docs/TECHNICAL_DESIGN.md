# AirGap Mirror 系统技术设计

版本：0.2

## 1. 系统定位

AirGap Mirror 是一个面向物理隔离网络的软件开发制品镜像同步平台。公网侧由 Windows 桌面客户端完成源分析、差异计算、TB 级分批下载和离线介质写入；内网侧由同一桌面客户端连接 Mirror Agent，将 Batch 分批上传并提交。Apache2 保持现有职责，直接从普通文件目录发布 APT、PyPI、Maven 与 npm tarball。

## 2. 核心原则

1. 文件系统中的标准仓库目录是仓库真实数据。
2. SQLite 只保存管理状态、任务状态和可重建 Catalog。
3. Epoch、Batch、Publish Unit 三层分离。
4. 一个 Epoch 可以跨多个 Batch、多块硬盘、多天完成；Batch 可乱序导入。
5. Epoch 的 target cursor 与 TotalBatches 一旦冻结不可变化。
6. Artifact 先导入，Metadata 最后发布。
7. 每个 Batch manifest 都携带整个 Epoch 的 PublishUnit 不可变声明，服务器可从任意 Batch 建立完整门禁。
8. 同路径不同 hash 是冲突，不做静默覆盖。
9. 不复制整个 live 仓库形成 staging。
10. 每种生态保留自身协议和目录结构。

## 3. 逻辑架构

```text
Internet upstreams
      |
      v
MirrorSync.exe (Windows/Wails)
  State Capsule Reader -> Adapter Analyze -> Epoch/Batch Planner
  -> Download Scheduler -> AGP Pack Writer
      |
====== air-gap ======
      |
      v
MirrorSync.exe -> Mirror Agent (Go control plane)
                         |
                         +--> npm metadata gateway
                         |
                         v
                 Repository filesystem
                         |
                         v
                       Apache2
```

## 4. 核心领域

- Source：生态、provider、上游 URL、仓库根目录、公开 URL、provider config。
- Cursor：Adapter 私有不可解释 token。
- SourceState：当前已发布 cursor、Catalog 版本、容量统计、Active Epoch。
- Epoch：BaseCursor -> frozen TargetCursor，固化 TotalBytes/TotalObjects/TotalBatches。
- PublishUnit：跨 Batch 的最小一致发布单元，Required 为整个 Epoch 不可变总数。
- Batch：移动介质容量单元，只是传输分组，不定义发布顺序。
- Pack：默认 32 GiB 的传输容器；单 artifact 不跨 Pack。
- Artifact：logical path、size、内部 SHA-256、upstream integrity、operation、metadata 标记。

Epoch 状态：`PLANNED -> DOWNLOADING -> TRANSFERRING -> PUBLISHABLE -> PUBLISHED -> COMPLETE`，终止态 `FAILED | CANCELLED`。

Batch 状态：`PLANNED -> DOWNLOADING -> VERIFYING -> READY -> TRANSFERRING -> IMPORTING -> IMPORTED`，异常态 `FAILED`。

## 5. State Capsule

```text
mirror-state-<id>.mstate (zip)
  state.json
  catalogs/<source-id>.sqlite
```

State Capsule 导出 Source 的公网分析所需配置、SourceState、Catalog snapshot 摘要；不导出仓库绝对 root path、Bearer token 或其他内网凭据。

## 6. Batch Bundle

```text
batch-<batch-id>/
  batch.json
  manifest.sqlite
  packs/000001.agp ...
```

`batch.json` 固化 source/epoch/batch identity、Base/Target Cursor、Epoch TotalBatches、Batch/Pack 汇总和 manifest hash。

`manifest.sqlite` 同时保存：
- 当前 Batch entries 与 pack offset/record length；
- 整个 Epoch 的 PublishUnit immutable declarations（id/key/required_count）。

## 7. AGP Pack v1

Pack Header 128 bytes；Record Header 80 bytes；Little Endian。Magic：`AGMPACK1` / `AGMREC01`。Record 保存 entry UUID、content length、path length、SHA-256，随后是 UTF-8 relative logical path 与 raw content。

约束：禁止绝对路径、`..`、NUL、Windows drive prefix；单 record 不跨 Pack；artifact 大于目标 Pack 时独占 oversized Pack；Pack 不整体压缩。

## 8. SQLite

Server DB：sources、source_states、epochs、batches、packs、publish_units、publish_metadata_entries、catalog_entries、import_sessions、imported_entries、audit_logs。

Client DB：state_capsules、download_epochs、planned_publish_units、planned_artifacts、download_batches、download_packs、download_entries、download_attempts、client_settings。

SQLite 不保存制品二进制。

## 9. Adapter 契约

所有 Adapter 通过流式 `PlanSink` 输出 PublishUnit/Artifact，不返回百万级 slice。通用 Planner/Downloader/Pack/Importer/Publisher 不复制到 Adapter。

- APT：权威 Release/InRelease 冻结 Target；解析 Packages/Sources；pool artifact first；原始签名 metadata last。
- PyPI：镜像 serial 只做变更枚举，PEP 691 JSON 获取变化项目详情；project 为 PublishUnit。
- npm：replication `_changes` 到冻结 sequence，随后 fetch packument；tarball 用 SRI/SHA-1 验上游并计算内部 SHA-256；package 为 PublishUnit。
- Maven Generic：只有 provider 提供可枚举 index/manifest/API 时允许 full/incremental。
- Maven Central：默认 dependency-set，不开放未经授权的整体 mirror。

## 10. Server API v1

- `GET /api/v1/health`
- `GET /api/v1/capacity`
- Source CRUD 与 state/inventory
- State Capsule export/download
- Epoch/Batch read APIs
- Import Session：manifest 分块上传、Pack HEAD/PATCH 续传、Pack commit、Batch complete
- npm metadata gateway

Artifact 静态下载不经过 Agent。

## 11. Windows UI

三个一级页面：
1. 源状态：Agent 连接、Source/Cursor/容量/Epoch、State Capsule 导出。
2. 互联网同步：导入 State Capsule、Analyze、Batch/Pack 参数、目标盘容量、下载/暂停/恢复。
3. 服务器更新：本地 Batch 校验、Agent 容量门禁、Pack 续传/commit、PublishUnit 状态。

## 12. 性能与安全

- 单 Source 目标 >= 20 TiB，>= 10,000,000 Catalog entries。
- Batch 默认 2 TiB；Pack 默认 32 GiB；上传 chunk 默认 64 MiB。
- 服务器 transfer staging 峰值约一个 Pack，不复制整个 Batch/live repo。
- artifact 永不执行；logical path 防穿越；artifact/pack/manifest 都做 hash 验证。
- 同路径不同 hash 硬冲突；同 Source 同时只允许一个 Active Epoch。
- 管理 API 使用 bearer token；Apache 数据面认证独立。
