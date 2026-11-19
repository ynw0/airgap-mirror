# AirGap Mirror 源码审查与落地计划（2026-11）

> 本文以实际代码行为为准。README / IMPLEMENTATION_PLAN 中的完成度是历史设计声明，不等同于可复现的生产验收结果。

## 1. 必须保留的产品边界

- 系统解决的是 **公网侧分析与冻结 → 可移动介质多批次搬运 → 隔离侧可靠导入 → Apache 原生仓库发布**，而不是通用 HTTP 代理或内网主动拉取。
- State Capsule 是单次分析的权威基线；Epoch 冻结 base/target cursor；Batch 可乱序、跨天导入；AGP/Manifest 以 digest 校验完整性。
- artifact 不允许静默覆盖不同内容；metadata 是唯一允许 UPDATE/DELETE 的同步数据，必须在相应依赖就绪后发布。
- APT/PyPI/npm/Maven 各有上游一致性和发布语义；不能用一个无差异的目录镜像算法替代适配器。
- Linux Agent 的 HTTP 管理面与 Apache 的仓库数据面分离。生产部署不得将 staging、SQLite、control token 作为 Apache 静态文件暴露。

## 2. 已验证问题及本 PR 的修复

| 级别 | 代码位置 | 现象 | 处理 |
| --- | --- | --- | --- |
| P0 | `internal/adapters/apt/adapter.go` | 同一个 PublishUnit 先声明 Required=0，再修改为 N；SQLite 禁止修改不可变声明 | 仅在制品数量确定后声明 |
| P0 | `internal/adapters/pypi/adapter.go` | 同 APT，可让 PyPI 首次分析/增量分析失败 | 仅在制品数量确定后声明 |
| P0 | `internal/client/workspace.go` | 无变化分析保存 PLANNED Epoch 后直接返回；后续分析被幽灵任务阻断 | 使用严格前置条件的本地 Noop 完成操作 |
| P0 | `internal/service/downloader.go` | 部分下载文件末尾比 SQLite checkpoint 新时拒绝断点恢复；进度落库前未 fsync | 仅从可持久恢复的 checkpoint 续传 |
| P1 | `internal/service/http_fetcher.go` | 接受缺失 Content-Range 的 206；不对比续传后的 ETag | 强制校验 Range/实体一致性 |
| P1 | `internal/service/transfer.go` | 外来 descriptor 可包含负 BatchBytes，转 uint64 后失真；pack 声明验证过弱 | 在申请目录/容量检查前验证所有计数、SHA、pack 结构 |
| P1 | `.github/workflows/ci.yml` | CI 只有编译，不执行 Go tests；main 原有 gofmt 失败 | 增加 Go 测试门禁并修复已知格式问题 |

## 3. 尚未完成的生产级风险

### 正确性与安全性

- **APT 信任链**：InRelease/Release.gpg 目前只提取文本和散列，没有基于受信任 keyring 的 OpenPGP 验签，尚不能对上游被篡改作认证声明。应支持来源专属信任根、签名验证、Valid-Until/时间策略，以及密钥轮换。
- **断点续传并发**：`appendChunk` 的磁盘 offset 检查和写入与 SQLite offset 更新尚无单一线性化点；并发 PATCH 同一 Pack 可能竞争。应实现每 session/pack 的排他租约或单 writer + CAS 状态机，并配并发测试。
- **publish 原子性**：当前 PublishUnit 逐文件 rename，业务上仍可能观察到更新中间态。应按仓库类型设计快照目录/原子切换或使用不可变 by-hash 引用；验证 Apache 缓存一致性。
- **崩溃恢复**：Pack 记录、SQLite 计数、目录 fsync、metadata 发布状态、GC 删除之间要有系统性的 failpoint/kill -9 测试和恢复算法。
- **外部载荷可信度**：Manifest 是非可信 SQLite 文件；除了哈希还需上限、SQL schema/索引约束、磁盘空间、压缩炸弹/恶意大字段防护和输入安全审查。

### 规模与资源

- `PublishReady` 在每次 Pack 导入后取回整个 Epoch 的 PublishUnit 列表；大量 Pack + 单元的组合导致重复 O(Pack × Unit) 扫描与内存压力。应改成状态驱动的 ready 队列或分批索引查询。
- `BundleFinalizer.Finalize` 为每个 Batch 都装载整个 Epoch 的 publish unit 声明；跨多 Batch 会重复制造 Manifest 元数据，规模化前应换成独立 Epoch 声明文件或分区引用清单。
- `ListEpochs`、部分 UI/API 和维护接口欠缺统一的分页、速率限制、可恢复游标与资源配额。
- 20 TiB / 千万制品目标需要明确外存排序、批量 SQLite 事务、WAL/checkpoint 策略、容量预估、Pack 最大记录尺寸、对象数量上限和文件系统 inode 压力的压测基准。

## 4. 建议按验收闭环推进

| 阶段 | 交付定义 | 必须通过的验证 |
| --- | --- | --- |
| P0 — 流程正确 | 四生态完成一次真正从公网到内网 Apache 的同步，重复分析/导入不报错 | 首次/二次分析、空增量、不同 Batch 顺序、重复请求 |
| P1 — 可恢复 | 下载、上传、导入、发布可在中断后恢复且不可丢数据 | 进程崩溃、断电模拟、文件截断、同名冲突、并发 PATCH |
| P2 — 可信供应链 | 网络输入与搬运数据完整性/来源验证 | APT 签名失败拒绝、篡改 Manifest/Pack 拒绝、路径逃逸拒绝 |
| P3 — 规模验证 | 实现有界内存和稳定数据库操作 | 百万/千万模拟制品、TB 规模磁盘 I/O、长时间 GC/Inventory |
| P4 — 运维交付 | 安全的 Apache/Agent 部署升级和恢复 | 清洁机器部署、备份恢复、权限隔离、监控告警、UI 操作验收 |

## 5. 本 PR 的边界

本 PR 优先修补可直接定位到代码契约的逻辑错误、可恢复性错误和 CI 盲区，补充回归测试；**不表示已完成 APT 签名验证、全系统断点恢复或 20 TiB 压测**。各项 P0–P4 应以自动化测试报告和可复现环境记录验收，不能凭文档打勾认定完成。
