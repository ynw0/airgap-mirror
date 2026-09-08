# AirGap Mirror 系统技术设计

版本：0.3

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
11. Inventory/GC 是显式 Maintenance，不进入正常增量同步热路径。
12. Active Epoch 与 Active Maintenance Job 在数据库边界互斥。

## 3. 逻辑架构

```text
Internet upstreams
      |
      v
Airgap Mirror.exe (Windows/Wails)
  State Capsule Reader -> Adapter Analyze -> Epoch/Batch Planner
  -> Download Scheduler -> AGP Pack Writer
      |
====== air-gap ======
      |
      v
Airgap Mirror.exe -> Mirror Agent (Go control plane)
                         |
                         +--> npm metadata gateway
                         +--> Inventory / GC maintenance
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
- Pack：默认约 32 GiB 的传输容器；单 artifact 不跨 Pack。
- Artifact：logical path、size、内部 SHA-256、upstream integrity、operation、metadata 标记。
- MaintenanceJob：Inventory/GC 长任务及扫描、候选、删除统计。
- GCCandidate：logical path、size、扫描时 mtime，持久到独立 candidate SQLite。

Epoch 状态：`PLANNED -> DOWNLOADING -> TRANSFERRING -> PUBLISHABLE -> PUBLISHED -> COMPLETE`，终止态 `FAILED | CANCELLED`。

Batch 状态：`PLANNED -> DOWNLOADING -> VERIFYING -> READY -> TRANSFERRING -> IMPORTING -> IMPORTED`，异常态 `FAILED`。

Maintenance 状态：`QUEUED -> RUNNING -> COMPLETED | FAILED | CANCELLED`。

## 5. State Capsule

```text
mirror-state-<id>.mstate (zip)
  state.json
  catalogs/<source-id>.sqlite
```

State Capsule 导出 Source 的公网分析所需配置、SourceState、Catalog snapshot 摘要；不导出仓库绝对 root path、Bearer token 或其他内网凭据。公网客户端直接以 read-only SQLite snapshot 读取 Catalog，不复制千万级记录到 Client 主库。

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
- 整个 Epoch 的 PublishUnit immutable declarations。

## 7. AGP Pack v1

Pack Header 128 bytes；Record Header 80 bytes；Little Endian。Magic：`AGMPACK1` / `AGMREC01`。Record 保存 entry UUID、content length、path length、SHA-256，随后是 UTF-8 relative logical path 与 raw content。

约束：禁止绝对路径、`..`、NUL、Windows drive prefix；单 record 不跨 Pack；artifact 大于目标 Pack 时独占 oversized Pack；Pack 不整体压缩。ResumeWriter 只保留完整 record，截断不完整尾部。

## 8. SQLite

Server DB 主要表：sources、source_states、epochs、batches、packs、publish_units、publish_metadata_entries、catalog_entries、import_sessions、imported_entries、audit_logs、maintenance_jobs。

Client DB 主要表：state_capsules、download_epochs、planned_publish_units、planned_artifacts、download_batches、download_packs、download_entries、download_attempts、client_settings。

Maintenance 额外使用短生命周期独立 SQLite：

- `catalog-<job>.sqlite`：Inventory rebuild；
- `gc-<job>.sqlite`：GC candidate plan。

SQLite 不保存制品二进制。

## 9. Adapter 契约

所有 Adapter 通过流式 `PlanSink` 输出 PublishUnit/Artifact，不返回百万级 slice。通用 Planner/Downloader/Pack/Importer/Publisher 不复制到 Adapter。

- APT：权威 Release/InRelease 冻结 Target；解析 Packages/Sources；pool artifact first；签名 metadata last。
- PyPI：Simple API serial 做变更枚举，project JSON 获取变化项目详情；project 为 PublishUnit。
- npm：replication `_changes` 到冻结 sequence，随后 fetch packument；tarball 验 SRI 并计算内部 SHA-256；package 为 PublishUnit。
- Maven Generic：只有 provider 提供可枚举 index/manifest/API 时允许 full/incremental；filesystem Inventory 必须显式配置。
- Maven Central：dependency-set，仅 exact release coordinates；不开放整体 mirror。

Inventory Adapter 通过 `CatalogBuildSink` 流式写隔离 Catalog。GC Adapter 预编译 `ManagedPathMatcher`，filesystem walk 热路径不反复解析 provider config。

## 10. Maintenance / Inventory / GC

### Inventory

```text
POST inventory
   -> Maintenance QUEUED/RUNNING
   -> Adapter Inventory
   -> catalog-<job>.sqlite
   -> stats 一致性检查
   -> 再次确认无 Active Epoch
   -> 单事务替换 source Catalog
   -> catalog_version + 1
```

扫描失败、中断或 Agent 重启不会写入半个 live Catalog。Agent 启动时遗留的 QUEUED/RUNNING maintenance job 标记为 FAILED，要求显式重新发起。

### GC

GC 默认 `execute=false`：

```text
filesystem walk
 -> Adapter managed-path matcher
 -> Catalog membership
 -> gc-<job>.sqlite
 -> COMPLETED
```

`execute=true` 仍重新扫描，不直接复用旧 dry-run 结果。执行阶段逐 candidate 再检查 Catalog membership、exclusive root、regular file、size、mtime。Linux 使用 `openat/fstatat/unlinkat` 与 `O_NOFOLLOW`，防止路径在扫描与删除之间被 symlink 替换。

物理 GC 要求 Source root 与其他 Source root 不相同、不嵌套、解析 symlink 后也不重叠。APT `pool/` 可能被多个 suite 共用，只有明确 `gcOwnsPool=true` 才视为该 Source 的 GC ownership。

## 11. Server API v1

控制面主要 API：

- `GET /api/v1/health`
- `GET /api/v1/capacity`
- Source CRUD / state
- State Capsule export/download
- Epoch/Batch read APIs
- Import Session：manifest HEAD/PATCH/complete、Pack HEAD/PATCH/commit、Batch complete
- `POST /api/v1/sources/{id}/inventory`
- `POST /api/v1/sources/{id}/gc`
- `GET /api/v1/maintenance`
- `GET /api/v1/maintenance/{id}`
- `POST /api/v1/maintenance/{id}/cancel`
- `GET /api/v1/maintenance/{id}/candidates`
- npm metadata gateway `/npm/{sourceID}/...`

Artifact 静态下载不经过 Agent。

## 12. Windows UI

保持三个一级页面：

1. **源状态**：Agent 连接、Source/Cursor/容量/Epoch、State Capsule 导出、Inventory、GC dry-run/candidates/execute。
2. **互联网同步**：导入 State Capsule、Probe/Analyze、Batch/Pack 参数、目标盘容量、下载/暂停/恢复。
3. **服务器更新**：本地 Batch 校验、Agent 容量门禁、Pack 续传/commit、服务器发布与状态对账。

React 是薄 UI：不直接访问 SQLite、不实现 Pack/HTTP 同步协议，业务调用均通过 Go binding。

## 13. 性能与安全

- 设计目标：单 Source >= 20 TiB，>= 10,000,000 Catalog entries。
- Batch 默认约 2 TiB；Pack 默认约 32 GiB；上传 chunk 默认 64 MiB。
- 服务器 transfer staging 峰值按 Pack 控制，不复制整个 Batch/live repo。
- 下载临时峰值按活跃 artifact `.part` 控制，不先落完整 Batch 再 repack。
- artifact 永不执行；logical path 防穿越；artifact/pack/manifest 都做完整性验证。
- 同路径不同 hash 硬冲突；同 Source 同时只允许一个 Active Epoch 或一个 Active Maintenance。
- 管理 API 使用 bearer token；Apache 数据面认证独立。
- GC physical delete 只支持 Linux；非 Linux 明确拒绝执行。

## 14. Build / Release

永久 Build workflow 对 main/PR 执行 Go formatting/module consistency、Linux control-plane build、React production build、Windows Wails EXE build和部署脚本语法检查；不隐式运行测试。

`v*` tag 触发 Release workflow，生成 Linux amd64 bundle、Windows amd64 ZIP 与各自 SHA-256，并发布 GitHub Release。
