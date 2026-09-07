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

## 阶段

### P0：领域模型与状态机 — 置信度 99/100

实现 Source、SourceState、Epoch、Batch、Pack、Artifact、PublishUnit、Cursor、StateCapsule；锁定 Epoch/Batch/Publish 状态迁移。

完成条件：核心模型不依赖具体生态；状态非法迁移返回明确错误；Epoch 固化 TotalBatches；PublishUnit 固化整个 Epoch 的 Required 数量。

### P1：SQLite 持久化 — 置信度 97/100

服务端数据库保存配置、Catalog、Epoch/Batch/Import/Audit；客户端数据库保存导入的 State Capsule、下载任务、Pack/Entry 进度。软件包本体不入库。

完成条件：schema 可幂等初始化；Repository 接口与 SQL 表一一对应。

### P2：Pack 与 Manifest — 置信度 96/100

定义 `.agp` Pack v1，默认目标 32 GiB/Pack；Batch 可由多个 Pack 组成。详细 entry 位置信息进入 `manifest.sqlite`，`batch.json` 保存批次摘要和 Pack 哈希；每个 Batch 的 manifest 同时携带整个 Epoch 的 PublishUnit 全局声明，使 Batch 可以乱序进入内网。

完成条件：支持顺序写/随机读取；单个 artifact 可独立 SHA-256 校验；Pack 可独立校验与导入。

### P3：Mirror Agent — 置信度 98/100

实现 Source/State/Epoch/Batch API、State Capsule 导出、Import Session、Pack 分片上传、Pack commit、Batch complete、健康检查和容量查询。

完成条件：Apache 不经过 Agent 下载 artifact；Agent 只负责控制面和 npm metadata gateway。

### P4：Adapter 契约 — 置信度 96/100

统一 SourceAdapter 接口，按生态实现能力：APT、PyPI、npm、Maven。四个 Adapter 不复制调度、Batch、Pack、导入代码。

### P5：Windows Wails UI — 置信度 98/100

三个一级页面：源状态、互联网同步、服务器更新。无 mock/fake 仓库数据；所有数据来自 Go binding 或真实配置。

### P6：部署与现有 Apache 集成 — 置信度 99/100

提供 Agent YAML 示例、systemd unit、Apache Alias/ProxyPass 示例和仓库目录布局。

### P7：Build 验证 — 置信度 95/100

只运行编译与前端 production build，不运行测试。测试在用户确认后单独执行。

## 架构风险

| 风险 | 风险度 | 处理 |
|---|---|---|
| TB 级批次中断 | 中 | Epoch/Batch/Pack 状态持久化；按 Pack 续传 |
| 几百万小文件搬运效率低 | 高 | 传输介质使用 16–32 GiB `.agp` Pack |
| APT 元数据提前发布引用缺失 `.deb` | 高 | `pool` artifact first，Release/InRelease last |
| PyPI/npm metadata 引用未导入文件 | 高 | PublishUnit 完整性门禁 |
| Maven Central 整体镜像限制 | 高 | Central provider 不开放无授权 full mirror |
| 同路径不同内容 | 高 | hash conflict 直接阻断，不覆盖 |
| SQLite Catalog 与文件系统偏离 | 中 | 文件系统为真相；提供显式 Rebuild Catalog |
| 服务器磁盘被单次导入打满 | 高 | 导入前 Capacity Gate；峰值只保留当前 Pack |
