# AirGap Mirror 实施 Plan

## 目标

构建一个面向物理隔离网络的软件开发源离线镜像系统。公网 Windows 客户端读取内网导出的状态胶囊，冻结一次同步 Epoch，将 TB 级差异拆成多个 Batch 下载到移动介质；进入内网后再逐批导入 Mirror Agent。现有 Apache2 继续直接发布 APT、PyPI、Maven 和 npm tarball 文件。

## 不改变的边界

- 现有 Apache2 与仓库目录继续作为数据面。
- 文件系统中的标准仓库目录是真实数据；SQLite Catalog 是可重建索引，不是软件包真实来源。
- 不为四种生态重新实现一套统一仓库协议。
- 不以 PostgreSQL 保存软件包记录或二进制。
- 不复制几十 TB live 仓库建立完整 staging。
- 不使用 fallback 掩盖源状态、hash 或发布冲突。

## 实施状态

截至 2026-09-08，P0–P8 的实现代码均已进入 `main`。验证方式是 formatting/module/build/configuration gate；自动化测试仍保持为单独阶段，未混入构建门禁。

| 阶段 | 状态 | 主要完成内容 |
|---|---|---|
| P0 领域模型与状态机 | 完成 | Source/Epoch/Batch/Pack/PublishUnit/Maintenance 状态机 |
| P1 SQLite 持久化 | 完成 | Server/Client DB、Catalog、Import、Maintenance、GC candidate |
| P2 Pack 与 Manifest | 完成 | AGP Pack v1、ResumeWriter、manifest.sqlite、Batch descriptor |
| P3 Mirror Agent | 完成 | Source/State/Import/Upload/Publish/Capacity/State Capsule API |
| P4 Adapter | 完成 | APT、PyPI、npm、Maven Generic、Maven Central dependency-set |
| P5 Windows Wails UI | 完成 | 三页面、断点下载/上传、维护面板、Windows EXE build |
| P6 Apache/systemd 部署 | 完成 | Apache control/data plane 集成、systemd、安装脚本 |
| P7 Inventory / GC | 完成 | 隔离 Catalog rebuild、原子切换、dry-run、secure Linux GC |
| P8 Build / Release | 完成 | 永久 Build workflow 与 `v*` tagged Release workflow |

## 阶段说明

### P0：领域模型与状态机 — 置信度 99/100

实现 Source、SourceState、Epoch、Batch、Pack、Artifact、PublishUnit、Cursor、StateCapsule、MaintenanceJob；非法状态迁移明确拒绝。

### P1：SQLite 持久化 — 置信度 97/100

服务端数据库保存配置、Catalog、Epoch/Batch/Import/Maintenance；客户端数据库保存 State Capsule、分析计划、下载和 Pack/Entry 进度。软件包本体不入库。

### P2：Pack 与 Manifest — 置信度 96/100

`.agp` Pack v1 默认目标约 32 GiB/Pack；Batch 默认约 2 TiB。详细 entry 位置进入 `manifest.sqlite`，`batch.json` 保存批次摘要和 Pack hash。单 artifact 不跨 Pack。

### P3：Mirror Agent — 置信度 98/100

实现 Source/State/Epoch/Batch、State Capsule、Import Session、Manifest/Pack resumable upload、Pack commit、Batch complete、容量查询和 npm metadata gateway。Artifact 静态下载不经过 Agent。

### P4：Adapter 契约 — 置信度 96/100

四种生态复用统一 Planner/Downloader/Pack/Importer/Publisher；Adapter 只实现协议语义。Maven Central 明确限制在 dependency-set。

### P5：Windows Wails UI — 置信度 98/100

三个一级页面：源状态、互联网同步、服务器更新。维护能力嵌入源状态页，不增加第二套业务实现。React 不直接访问 SQLite 或拼装同步协议。

### P6：部署与现有 Apache 集成 — 置信度 99/100

Agent 仅监听 loopback，Apache 反代控制 API/npm packument gateway，原生仓库继续静态发布。安装脚本不移动已有仓库。

### P7：Inventory / GC — 置信度 97/100

Inventory 写独立临时 Catalog，完整成功后才原子替换 live Catalog。Maintenance 与 Active Epoch 在 SQLite 边界互斥。GC 先生成持久候选数据库；`execute=false` 零删除，`execute=true` 重新扫描并在删除前再次检查 Catalog、root ownership、regular file、size 和 mtime。Linux 使用 `openat/fstatat/unlinkat` 与 `O_NOFOLLOW`。

APT 的 `pool/` 可能跨 suite 共用，因此只有明确配置 `gcOwnsPool=true` 时才进入 APT physical GC 范围。

### P8：Build / Release — 置信度 98/100

永久 CI 只做可重复的构建门禁，不修改仓库、不运行测试。`v*` tag 才创建 Linux/Windows release bundle 和 SHA-256 文件。

## 仍属于上线前验证而非功能开发的事项

- 在真实几十 TB 仓库副本上做容量、吞吐和长时间中断恢复验证。
- 在真实 APT/PyPI/npm/Maven 镜像数据上做端到端同步验收。
- 在确认测试策略后运行自动化/集成测试。

这些事项不改变当前架构或核心模块，只验证运行环境与边界条件。

## 架构风险

| 风险 | 风险度 | 处理 |
|---|---|---|
| TB 级批次中断 | 中 | Epoch/Batch/Pack 状态持久化；HTTP Range / resumable upload |
| 几百万小文件搬运效率低 | 高 | 移动介质使用大 `.agp` Pack |
| APT 元数据提前引用缺失 `.deb` | 高 | artifact first，Release/InRelease last |
| PyPI/npm metadata 引用未导入文件 | 高 | PublishUnit 完整性门禁 |
| Maven Central 整体镜像限制 | 高 | Central provider 只做 dependency-set |
| 同路径不同内容 | 高 | hash conflict 直接阻断，不覆盖 |
| Catalog 与文件系统偏离 | 中 | 显式 Inventory，隔离 rebuild 后原子切换 |
| GC 误删共享仓库文件 | 高 | exclusive root、Adapter ownership、APT pool opt-in、二次 membership/stat 校验 |
| 服务器磁盘被单次导入打满 | 高 | Capacity Gate；staging 峰值按 Pack 控制 |
