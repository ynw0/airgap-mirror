# AirGap Mirror

AirGap Mirror 是面向物理隔离网络的软件开发制品镜像同步平台。

公网 Windows 客户端读取内网导出的 State Capsule，按冻结的 Epoch 计算增量，将 TB 级差异拆分为可搬运 Batch 与大文件 Pack；进入内网后，同一客户端连接 Mirror Agent，续传并提交 Batch。Apache2 继续直接发布仓库文件，Mirror Agent 只负责控制面、导入、发布门禁、状态和 npm metadata gateway。

首期支持：APT、PyPI、npm、Maven。

详细设计见 `docs/TECHNICAL_DESIGN.md`，实施顺序见 `docs/IMPLEMENTATION_PLAN.md`。
