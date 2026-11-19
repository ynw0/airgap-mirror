# AirGap Mirror

AirGap Mirror 用来给**不能直接访问互联网的服务器**更新软件包。内网服务器导出一份状态文件，联网电脑按这份文件下载更新，再用移动硬盘把文件带回内网。

不需要让内网服务器直接上网，也不需要每次都搬运整个软件仓库。

## 一次更新怎么操作？

1. **在内网导出状态。** 在 Windows 客户端打开“连接服务器”，输入 Mirror Agent 地址和令牌。勾选需要更新的软件源，把状态文件（`.mstate`）保存到移动硬盘。
2. **在联网电脑下载。** 把移动硬盘带到能上网的 Windows 电脑，打开“联网下载”，选择刚才的状态文件和软件源。点击“查看需要下载的更新”，再把各批次下载到移动硬盘。中断后可以继续。
3. **回到内网导入。** 把下载好的批次文件夹带回内网电脑，在“离线导入”页面检查文件并导入服务器。如果有多个批次，就逐批导入。

两台 Windows 电脑可以不同：导入时可以直接读取移动硬盘中的批次文件，不要求使用原来下载时的电脑。

## 需要准备什么？

- **内网 Linux 服务器：** 运行 Mirror Agent，接收更新并管理仓库。Apache 可以继续提供软件包下载服务。
- **Windows 电脑：** 运行同一套桌面客户端，分别用于联网下载和内网导入。
- **移动硬盘或 U 盘：** 带出状态文件，带回下载好的文件。注意介质剩余空间及文件系统限制。

项目包含 APT、PyPI、npm 和 Maven 的适配器。Maven Central 同步的是指定依赖列表，不是整个中央仓库。

## 部署 Linux Agent

在项目根目录构建：

```bash
go build -o bin/mirror-agent ./cmd/mirror-agent
go build -o bin/mirrorctl ./cmd/mirrorctl
```

安装：

```bash
sudo ./scripts/install-agent.sh
```

启动 Agent 需要设置连接令牌 `AIRGAP_MIRROR_TOKEN`，并在服务器上配置软件源及其仓库目录。Apache 和 systemd 的配置示例见 [部署说明](docs/DEPLOYMENT.md)。

## 遇到问题时

- **连不上服务器：** 检查 Agent 是否在运行、地址和端口是否正确，连接令牌是否一致。
- **下载中断：** 回到原来的下载任务，选择同一个保存位置继续。不要删除未下载完的文件包。
- **提示软件源正在同步：** 先完成已有任务。已经写入服务器的任务不能随意取消。
- **想释放服务器空间：** 展开“连接服务器”页面里的维护工具，先预览可能不再需要的文件，再决定是否删除。

## 实现和限制

Windows 客户端采用 Wails v2、React 和 TypeScript，Linux Agent 使用 Go；SQLite 保存同步状态，实际文件保存在原生仓库目录中。项目 CI 会运行 Go 测试、Linux 构建和 Windows 桌面端构建。

**注意：APT 适配器目前还没有执行上游 Release 文件的 OpenPGP 签名验证。** 文件散列校验不能代替来源认证，不能把当前实现当作已完成供应链签名验证的方案。

更多部署信息见 [部署说明](docs/DEPLOYMENT.md)。其他设计文档只供参考，实际行为以源码为准。

## Linux 服务器的浏览器管理界面

更新 Agent 后，在服务器上运行安装脚本，再用配置脚本设置实际监听地址：

```bash
sudo bash scripts/install-agent.sh
sudo bash internal/server/web/configure-agent.sh --listen 192.168.21.4:8787
```

如果 Linux 服务器的 IP 是 `192.168.21.4`，可从可信内网浏览器打开 `http://192.168.21.4:8787/`。这是示例地址，更换服务器时请使用对应 IP。页面需要输入 Agent 的连接令牌。配置脚本只设置 Agent 监听地址，不修改 Apache、仓库文件或防火墙规则。

**已经使用 Apache 反向代理的旧服务器请保留原来的域名。** 例如旧地址是 `https://mirror.costar.com/mirror-control`，新版 Linux WebUI 使用 `https://mirror.costar.com/mirror-control/`（结尾带 `/`），Windows 客户端仍可填写原来的 Agent URL。Agent 应继续只监听 `127.0.0.1:8787`，不需要运行 `--listen 192.168.21.4:8787`。Apache 的 `ProxyPass /mirror-control/ http://127.0.0.1:8787/` 保留不变。建议将无结尾斜杠的 `/mirror-control` 重定向到 `/mirror-control/`，避免浏览器相对路径失效。


以上命令适用于从 GitHub 源码仓库克隆并构建的情况。若使用 `v*` Linux 发布包，配置脚本位于 `scripts/configure-agent.sh` 和 `scripts/configure-apt-signing.sh`。两份脚本也可在已安装的 Linux Web 管理页面直接下载，再复制到离线服务器运行。

### 检查统计后为 APT 仓库重新签名

先从 Linux 管理页面运行“重新清点文件”，等清点任务完成并确认文件数与容量。需要重新签名时，先由管理员运行：

```bash
sudo bash internal/server/web/configure-apt-signing.sh
```

此脚本在 `/var/lib/airgap-mirror/gnupg` 为服务用户创建/选定专用密钥，管理员需要明确同意创建；不会偷偷在客户端添加信任。再回到 Web 管理页面点击“检查后重新签名”，重新生成内网 APT 的 `Release`、`Release.gpg` 和 `InRelease`。签完后下载公钥，按客户端仓库配置将其安装为专用受信任密钥（不要将私钥拷走）。

**重要：本地重新签名不等于验证公网 APT 上游签名。** 当前系统仍没有对上游 `InRelease` / `Release.gpg` 完成可信公钥验签。请不要把重新签名理解为对上游来源真实性的确认。

### Windows 下载恢复

程序现在会在打开联网下载页面时查找未完成的任务。原来已下载完成的批次可以保留；丢失或损坏的批次可以选择“重新下载这一批”。默认单批约 8 GiB、文件包约 1 GiB，适合更多移动硬盘。对于超过单个文件包大小的制品，仍须使用支持大文件的移动介质。


### 大型 PyPI 仓库分段清点与断点恢复

PyPI 清点按 `simple/<project>/index.html` 中的项目逐个检查，使用独立的 SQLite 临时账本。每完成 100 个项目保存一次原子检查点（项目位置、文件数量、容量及对应文件记录一起提交），已完成的检查点在暂停或 Agent 重启后保留。Linux WebUI 和 Windows 维护工具都可使用“继续清点”，或“放弃清点”删除临时账本。

- 只有全量检查成功后才用单次数据库事务替换正式 Catalog；暂停/异常期间旧 Catalog 保持不变。
- 检查点不是逐字节校验几十 TB 的包内容：主要解析项目索引、校验文件是否存在及大小；文件哈希取自项目索引。支持随时停止，但最后不足 100 个尚未提交的项目需要重查。
- 若源设置、项目目录成员或已检查项目的索引文件发生变化，为防止遗漏项目，会拒绝发布陈旧清单；需要放弃旧清点并重新开始。扫描期间不要在外部修改仓库目录。
- 暂停的任务继续占用这个软件源的维护位置，避免与下一轮导入混杂。若不再继续，请先点击“放弃清点”再开始新的同步。
