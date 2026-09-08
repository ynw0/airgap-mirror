# AirGap Mirror

AirGap Mirror 是面向物理隔离网络的软件开发制品镜像同步平台。

公网 Windows 客户端读取内网导出的 State Capsule，按冻结的 Epoch 计算增量，将 TB 级差异拆分为可搬运 Batch 与 AGP Pack；进入内网后，同一客户端连接 Mirror Agent，断点续传并提交 Batch。Apache2 继续直接发布仓库文件，Mirror Agent 只负责控制面、导入、发布门禁、维护任务和 npm metadata gateway。

首期支持：APT、PyPI、npm、Maven。

## 核心约束

- 仓库文件系统是真实数据面；SQLite 是状态、计划与可重建 Catalog。
- Epoch target cursor 创建后不可改变。
- 一个 Epoch 可跨多个 Batch、多块移动介质、多天完成，Batch 可乱序导入。
- Artifact 先导入，mutable metadata 最后发布。
- 同 logical path 不同 hash 为硬冲突，不静默覆盖。
- 传输使用 AGP Pack，避免移动介质处理数百万小文件。
- Maven Central 仅支持明确的 dependency-set，不提供未经授权的全量镜像模式。
- Inventory 在隔离 Catalog 中重建，完整成功后才原子替换 live Catalog。
- GC 默认 dry-run；物理删除必须重新扫描并再次验证 Catalog、路径、size 与 mtime。

## 数据流

```text
内网 Mirror Agent
  └─ 导出 State Capsule
          ↓ 物理介质
公网 Windows 客户端
  └─ Probe / Analyze / Epoch / Batch / Pack 下载
          ↓ 物理介质
内网 Windows 客户端
  └─ Bundle 校验 / resumable upload / commit
          ↓
Mirror Agent ── publish gate ──> 原生仓库目录 ──> Apache2
```

## Windows 客户端

Wails v2 + React/TypeScript，保持三个一级页面：

1. **源状态**：Agent、Cursor、容量、State Capsule，以及 Inventory/GC 维护。
2. **互联网同步**：导入 State Capsule、Probe/Analyze、Batch 规划、断点下载。
3. **服务器更新**：Bundle 校验、Pack 续传、commit、发布与状态对账。

## Linux Agent

构建：

```bash
go build -o dist/mirror-agent ./cmd/mirror-agent
go build -o dist/mirrorctl ./cmd/mirrorctl
```

安装：

```bash
sudo ./scripts/install-agent.sh ./dist/mirror-agent ./dist/mirrorctl
```

详细部署和 Source 配置见 `docs/DEPLOYMENT.md`。

## 项目结构

```text
cmd/                  Linux Agent / CLI
desktop/              Wails v2 Windows 客户端
internal/domain/       领域模型与状态机
internal/ports/        跨模块接口
internal/pack/         AGP Pack v1
internal/storage/      SQLite 状态库
internal/service/      规划、下载、导入、发布、Inventory/GC
internal/server/       HTTP 控制面与 npm gateway
internal/adapters/     APT / PyPI / npm / Maven
deploy/                systemd / Apache 示例
docs/                  技术设计、实施计划、部署说明
.github/workflows/     永久 Build / tagged Release
```

## 构建门禁与发布

`main` 和 Pull Request 由 `.github/workflows/ci.yml` 持续执行构建门禁：

- Go formatting / module consistency；
- Linux control-plane build；
- React production build；
- Windows Wails executable build；
- 安装脚本 shell syntax。

不在 CI 中隐式运行测试。测试属于单独阶段。

推送 `v*` tag 时，`.github/workflows/release.yml` 构建并发布：

- `airgap-mirror-linux-amd64.tar.gz`；
- `airgap-mirror-windows-amd64.zip`；
- 对应 SHA-256 文件。

详细设计见 `docs/TECHNICAL_DESIGN.md`，实施状态见 `docs/IMPLEMENTATION_PLAN.md`。
