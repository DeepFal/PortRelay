<p align="center">
  <img src="mac-native/AppIcon.iconset/icon_256x256.png" width="96" alt="PortRelay">
</p>

<h1 align="center">PortRelay</h1>

<p align="center">
  <b>把内网服务临时、可控地开放给同事：发一条邀请链接，到期自动收回。</b>
</p>

<p align="center">
  <a href="https://github.com/DeepFal/PortRelay/actions/workflows/ci.yml"><img src="https://github.com/DeepFal/PortRelay/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="https://github.com/DeepFal/PortRelay/releases"><img src="https://img.shields.io/github/v/release/DeepFal/PortRelay?sort=semver" alt="Release"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue.svg" alt="MIT"></a>
  <img src="https://img.shields.io/github/go-mod/go-version/DeepFal/PortRelay" alt="Go">
  <img src="https://img.shields.io/badge/platform-macOS%20%7C%20Linux%20%7C%20Windows-lightgrey" alt="Platform">
</p>

<p align="center">
  简体中文 · <a href="README_EN.md">English</a>
</p>

<p align="center">
  <img src="docs/images/architecture.svg" alt="PortRelay 架构" width="100%">
</p>

你的电脑能连上某个内网网段：数据库、跳板机、内部面板，但同事连不上。常见的做法是开 VPN 账号、改防火墙，或者干脆把端口对所有人放开，事后还经常忘记关。

PortRelay 把这件事变成三步：

1. **Agent** 把内网目标挂到 **边缘网关** 的一个端口上。
2. 你把**邀请链接**（或二维码）发给同事。
3. 同事打开链接、填写姓名，他的设备就获得一段时间的访问权。到期、闲置或被你撤销后，连接立即断开。

> [!TIP]
> **不依赖任何特定组网方案。** 只要 Agent 所在的机器**同时能连到目标网段和边缘网关**就能工作。Agent 到网关的线路随你选：同一局域网、公司 VPN、Tailscale、ZeroTier、WireGuard，或者网关本身有公网地址。Agent 只需主动连出，本机不用开放任何端口。

## 目录

- [功能](#功能)
- [界面一览](#界面一览)
- [工作原理](#工作原理)
- [快速开始](#快速开始)
- [配置参考](#配置参考)
- [安全模型](#安全模型)
- [常见问题](#常见问题)
- [开发](#开发)

## 功能

**访问控制**

- 三种访问方式：**仅授权链接**（默认，最安全）、IP / 网段白名单（可与授权链接叠加），以及全部放通。
- 邀请链接可以设置链接有效期、可用设备数、授权后可访问时长（1 小时、2 小时、8 小时、当天有效，或直到通道关闭），以及是否需要审批。
- 需要审批时，App 会弹出系统通知，一键批准或拒绝；访问者的页面会自动刷新结果。
- **计时从首次连接开始**：同事拿到授权后不用急着连，开始用的时候才开始计时。
- 授权到期或被撤销后，**已建立的连接也会立刻断开**；未授权的连接直接被重置（TCP RST）并留下拦截记录。

**通道生命周期**

- **闲置自动关闭**：没有连接、没有有效授权、没有待处理申请，也没有还能使用的邀请链接时开始计时，默认 30 分钟后关闭。
- 可选的**最长开启时长**，到点无论是否在用都关闭。
- 记录首次连接时间、实际连接时长和每次关闭的原因。
- 编辑通道不会打断现有连接；更换端口时先绑定新端口再释放旧端口。

**可观测**

- 每个通道近 10 分钟的流量曲线，以及实时速率、连接数、本次与历史累计流量。
- 在线设备按 IP 合并展示，可逐条断开；历史接入台账同样按 IP 合并。
- 按通道限速（MB/s）。

**客户端**

- 原生 macOS 菜单栏 App（SwiftUI）：关闭窗口只保留菜单栏图标，从菜单栏退出时连同后台 Agent 一起退出。
- 内嵌网页面板，Linux、Windows 上也能用同一套功能。
- 通道搜索（支持拼音首字母，比如输入 `sjk` 就能找到“数据库”），以及按运行状态筛选。

## 界面一览

<p align="center">
  <img src="docs/images/live-traffic.gif" alt="通道卡片与实时流量" width="100%">
  <br><sub>通道卡片：访问地址、授权状态、实际连接时长，以及近 10 分钟流量（动图为加速演示）</sub>
</p>

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/images/app-main-dark.png">
  <img src="docs/images/app-main-light.png" alt="PortRelay macOS App 主界面">
</picture>

**邀请与授权**：生成链接后得到一段可直接发送的文案和二维码。同事在手机或电脑上打开链接填写姓名，需要审批时进入等待页，批准后页面自动更新。

<p align="center">
  <img src="docs/images/app-invite.png" alt="邀请分享卡片" width="100%">
</p>

<table>
  <tr>
    <td align="center"><img src="docs/images/auth-form.png" alt="填写姓名" width="100%"><br><sub>① 打开邀请链接</sub></td>
    <td align="center"><img src="docs/images/auth-pending.png" alt="等待审批" width="100%"><br><sub>② 等待管理员批准（可选）</sub></td>
    <td align="center"><img src="docs/images/auth-ok.png" alt="授权成功" width="100%"><br><sub>③ 授权成功</sub></td>
  </tr>
</table>

**连接与授权管理**：待审批的申请、已授权设备（在线状态与剩余时间），以及仍然有效的邀请链接，都在同一处管理。

<p align="center">
  <img src="docs/images/app-access.png" alt="授权管理" width="80%">
</p>

<details>
<summary><b>网页面板与更多截图</b></summary>
<br>

不使用 macOS App 时，Agent 自带网页面板，地址为 `http://127.0.0.1:33080`：

<img src="docs/images/web-panel.png" alt="网页面板">

<table>
  <tr>
    <td align="center"><img src="docs/images/app-edit.png" alt="编辑通道"><br><sub>编辑通道：访问方式、闲置关闭、最长时长</sub></td>
    <td align="center"><img src="docs/images/app-clients.png" alt="在线设备"><br><sub>在线设备：按 IP 合并，可逐条断开</sub></td>
  </tr>
</table>

</details>

## 工作原理

<p align="center">
  <img src="docs/images/invite-flow.svg" alt="邀请流程" width="100%">
</p>

- **边缘网关**（`portrelay-server`）运行在访问者能连到的机器上，管理一个端口池（默认 `33000-34000`）。`33000` 是控制端口：Agent 的隧道和访问者打开的授权网页共用这一个端口，网关根据请求开头自动分流（HTTP 请求进入授权网页，其余视为 Agent 会话）。
- **Agent**（`portrelay-agent`，macOS App 内置）运行在能访问目标网段的机器上。它主动连接网关的控制端口，先用 token 认证，再通过 [yamux](https://github.com/hashicorp/yamux) 在这条 TCP 连接上多路复用所有通道。
- 访问者连接 `网关:33660` 时，网关先检查白名单和授权租约。通过后在隧道里开一个流，由 Agent 连向 `10.0.1.16:3306`，双向转发。不通过则直接重置连接并记录。
- 授权租约、邀请和审批请求保存在网关的状态文件里，重启后仍然有效。通道配置、流量历史和接入台账保存在 Agent 本机。

## 快速开始

### 1. 部署边缘网关（Linux）

网关需要满足两点：**访问者能连到它**，**Agent 也能连到它**。防火墙放通 TCP `33000-34000`。

**方式 A：二进制 + systemd（推荐）**

```bash
VERSION=1.0.0   # 见 Releases 页面
curl -LO https://github.com/DeepFal/PortRelay/releases/download/v$VERSION/portrelay-$VERSION-linux-amd64.tar.gz
tar xzf portrelay-$VERSION-linux-amd64.tar.gz && cd portrelay-$VERSION-linux-amd64
sudo ./deploy/install-server.sh ./portrelay-server

# 安装脚本会生成随机 token，记下来填到 Agent 里
sudo grep PORTRELAY_TOKEN /etc/portrelay-server.env
```

安装脚本会把程序装到 `/usr/local/bin`，写入 [systemd 单元](deploy/portrelay-server.service)（以临时系统用户运行，状态保存在 `/var/lib/portrelay`），并设置开机自启。再次运行同一个脚本即可升级，已有配置会保留。

**方式 B：Docker**

```bash
TOKEN=$(openssl rand -hex 24); echo "token: $TOKEN"
docker run -d --name portrelay --network host --restart unless-stopped \
  -e PORTRELAY_TOKEN=$TOKEN -v portrelay:/var/lib/portrelay \
  ghcr.io/deepfal/portrelay-server:latest
```

> [!IMPORTANT]
> 请使用 `--network host`。白名单依赖访问者的真实 IP，端口映射可能会把来源地址换成网桥地址。另见 [docker-compose 示例](deploy/docker-compose.yml)。

**方式 C：从源码构建**

```bash
git clone https://github.com/DeepFal/PortRelay.git && cd PortRelay
make server        # 产物：dist/portrelay-server-linux-{amd64,arm64}
```

### 2. 运行 Agent

**macOS App**：从 [Releases](https://github.com/DeepFal/PortRelay/releases) 下载 `PortRelay-*-macos-universal.zip`，解压后拖进“应用程序”。

> [!NOTE]
> App 未经 Apple 公证。首次打开时，在 Finder 中右键选择“打开”，或者执行 `xattr -dr com.apple.quarantine /Applications/PortRelay.app`。也可以 `make app` 自行构建。

打开后点右上角的 ⚙️ 设置：

| 设置项 | 填什么 |
| --- | --- |
| 服务器控制地址 | Agent 连接网关用的地址，例如 `gw.example.com:33000`、`192.168.1.10:33000` 或 `100.x.y.z:33000` |
| 安全令牌 | 网关上的 `PORTRELAY_TOKEN` |
| 对外访问地址 | **同事**访问网关用的地址，留空则与上面相同 |

> 举例：Agent 通过 Tailscale 地址 `100.64.0.10` 连网关，同事在办公室局域网里用 `192.168.1.10` 访问。这时“服务器控制地址”填 `100.64.0.10:33000`，“对外访问地址”填 `192.168.1.10`，邀请链接和分享文案都会使用后者。

**Linux / Windows**：下载对应平台的压缩包，直接运行 Agent，在浏览器打开的面板里完成同样的设置。

```bash
./portrelay-agent              # 面板：http://127.0.0.1:33080
./portrelay-agent -no-browser  # 不自动打开浏览器
```

### 3. 创建通道并邀请同事

1. 点 **添加通道**，填写名称和目标地址（如 `10.0.1.16:3306`）。可以先“探测”一下本机能否连通；端口会自动推荐。
2. 访问方式保持默认的 **仅授权链接**，然后点 **邀请**，选择授权后可访问时长、可用设备数，以及是否需要审批。
3. 把生成的文案或二维码发给同事。同事打开链接、填写姓名后，就可以用数据库客户端或 SSH 连接 `192.168.1.10:33660`。

## 配置参考

**网关 `portrelay-server`**

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `-token` / `PORTRELAY_TOKEN` | 必填 | Agent 认证用的共享密钥，建议用 `openssl rand -hex 24` 生成 |
| `-listen` | `:33000` | 控制端口（Agent 隧道与授权网页共用） |
| `-port-min` / `-port-max` | `33000` / `34000` | 通道端口池 |
| `-state` | `/var/lib/portrelay/leases.json` | 授权租约、邀请与审批的持久化文件，留空则只保存在内存 |
| `-public-host` | 空 | 授权页上展示给访问者的地址，默认取访问者打开链接时使用的地址 |
| `-trust-proxy` | `false` | 信任 `X-Forwarded-For` / `X-Real-IP`，**仅在网关前有可信反向代理时开启** |
| `-version` | | 打印版本 |

**Agent `portrelay-agent`**

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `-port` | `33080` | 本地面板 / API 端口（只监听 `127.0.0.1`） |
| `-no-browser` | `false` | 启动时不自动打开面板 |
| `-version` | | 打印版本 |

Agent 的配置保存在 `~/.portrelay/config.json`（权限 0600，原子写入；文件损坏时会先备份再使用默认值）。

## 安全模型

- **Agent 认证**：连接建立后的第一条消息必须是 token 认证（常量时间比较），认证通过的会话才能注册通道。新会话会替换旧会话。
- **默认拒绝**：新通道默认只允许持有授权的设备访问。授权按“通道 + 来源 IP”生效，并且有明确的到期时间。
- **到期即断**：网关每秒检查一次存量连接，授权失效的连接立即断开。
- **本地 API 只对本机开放**：面板只监听回环地址，并校验 `Host` 与 `Origin`，防止 DNS 重绑定和跨站请求。

> [!WARNING]
> - **Agent 与网关之间的链路目前没有加密**，token 与转发的数据都以明文传输。请让这段链路跑在可信网络或加密通道上（WireGuard、Tailscale、ZeroTier、IPSec VPN 等），或者在两端之间自行加一层 TLS 隧道。
> - 授权以**来源 IP** 为准。如果多人通过同一个 NAT 出口访问网关，他们会共享同一份授权。
> - 访问者到网关的流量就是目标服务本身的协议，加密与否取决于目标服务（例如 MySQL 是否启用 TLS）。

## 常见问题

<details>
<summary><b>必须用 Tailscale 吗？</b></summary>

不需要。PortRelay 只要求 Agent 所在机器能访问目标网段，并且能通过 TCP 连到网关的控制端口。Tailscale 只是让 Agent 连上网关的方式之一，局域网直连、VPN 或公网地址都可以。
</details>

<details>
<summary><b>和 frp、<code>ssh -R</code> 有什么区别？</b></summary>

它们都能做端口转发，而且更通用。PortRelay 专注于**临时、按人授权**的场景：默认拒绝、通过邀请链接按设备授权、可选审批、首次连接起计时、到期或闲置自动收回，并配有图形界面和接入台账。如果只需要一条长期开着的转发，frp 或 SSH 更简单。
</details>

<details>
<summary><b>一个网关能接多个 Agent 吗？</b></summary>

目前同一时间只服务一个 Agent，新的认证会话会替换旧会话。需要多个 Agent 时，可以部署多个网关实例（使用不同的端口池和 token）。
</details>

<details>
<summary><b>网关重启会怎样？</b></summary>

正在进行的连接会断开。授权租约、邀请链接和审批请求保存在状态文件里，重启后依然有效。Agent 会自动重连并重新注册通道。
</details>

<details>
<summary><b>访问者看到的 IP 不对 / 白名单不生效？</b></summary>

检查网关前面是否有 NAT、端口映射或反向代理。Docker 部署请使用 `--network host`。只有在可信反向代理之后才开启 `-trust-proxy`，否则 `X-Forwarded-For` 可以被伪造。
</details>

<details>
<summary><b>App 提示“无法验证开发者”？</b></summary>

发布的 App 只做了 ad-hoc 签名，没有经过 Apple 公证。请右键选择“打开”，或者执行 `xattr -dr com.apple.quarantine /Applications/PortRelay.app`。
</details>

## 开发

需要 Go 1.23+；构建 macOS App 还需要 Xcode 命令行工具，运行环境为 macOS 13 及以上。

```bash
make test         # gofmt 检查 + go vet + go test -race
make agent        # 编译当前平台的 Agent 到 bin/
make app          # 构建 PortRelay.app（UNIVERSAL=1 ./build-app.sh 构建通用二进制）
make release      # 打包全部平台的网关与 Agent 到 dist/
make snapshots    # 离屏渲染 App 各界面截图到 tools/ui-snapshots/out/
```

```text
cmd/portrelay-server   边缘网关入口
cmd/portrelay-agent    Agent 入口（内嵌网页面板 web/）
server/                网关：Agent 会话、端口监听、白名单与授权租约、邀请与授权网页
client/                Agent：配置、隧道、本地 API、闲置关闭、流量统计
core/                  协议消息、ACL、流量计量与限速
mac-native/            SwiftUI 菜单栏 App
deploy/                systemd、Docker Compose 部署示例与安装脚本
tools/ui-snapshots/    离屏截图工具与 README 动图生成（mkgif）
```

README 里的截图和动图都用模拟数据生成：`tools/ui-snapshots/render.sh readme` 渲染截图，`FRAMES=40 tools/ui-snapshots/render.sh gif` 渲染逐帧画面，再用 `go run ./tools/ui-snapshots/mkgif` 合成 GIF。

欢迎提交 Issue 和 Pull Request。提交前请先运行 `make test`。

## 许可证

[MIT](LICENSE) © DeepFal
