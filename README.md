# AirGap Mirror

AirGap Mirror 是面向物理隔离网络的软件开发制品镜像同步平台。

公网 Windows 客户端读取内网导出的 State Capsule，按冻结的 Epoch 计算增量，将 TB 级差异拆分为可搬运 Batch 与大文件 Pack；进入内网后，同一客户端连接 Mirror Agent，续传并提交 Batch。Apache2 继续直接发布仓库文件，Mirror Agent 只负责控制面、导入、发布门禁、状态和 npm metadata gateway。

首期支持：APT、PyPI、npm、Maven。

## 核心约束

- 仓库文件系统是真实数据面；SQLite 是状态、计划与可重建 Catalog。
- Epoch target cursor 创建后不可改变。
- 一个 Epoch 可跨多个 Batch、多块移动介质、多天完成，Batch 可乱序导入。
- Artifact 先导入，mutable metadata 最后发布。
- 同 logical path 不同 hash 为硬冲突，不静默覆盖。
- 传输使用 AGP Pack，避免移动介质处理数百万小文件。
- Maven Central 默认仅支持 dependency-set，不提供未经授权的全量镜像模式。

## 项目结构

```text
cmd/                  Linux Agent / CLI
desktop/              Wails v2 Windows 客户端
internal/domain/       领域模型与状态机
internal/ports/        跨模块接口
internal/pack/         AGP Pack v1
internal/storage/      SQLite 状态库
internal/service/      规划、下载、导入、发布
internal/server/       HTTP 控制面
internal/adapters/     APT / PyPI / npm / Maven
deploy/                systemd / Apache 示例
docs/                  技术设计与实施计划
```

详细设计见 `docs/TECHNICAL_DESIGN.md`，实施顺序见 `docs/IMPLEMENTATION_PLAN.md`。
