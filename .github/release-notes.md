
## 下载 · Downloads

### 客户端 · Client

装在能访问内网服务的电脑上，用来创建通道、邀请同事。
Install it on a machine that can reach your internal services.

| 平台 Platform | 文件 File |
| --- | --- |
| macOS（Apple Silicon / Intel） | [PortRelay-{{VERSION}}.dmg]({{DOWNLOAD}}/PortRelay-{{VERSION}}.dmg) |
| Linux x86_64 | [portrelay-agent-{{VERSION}}-linux-amd64.tar.gz]({{DOWNLOAD}}/portrelay-agent-{{VERSION}}-linux-amd64.tar.gz) |
| Linux ARM64 | [portrelay-agent-{{VERSION}}-linux-arm64.tar.gz]({{DOWNLOAD}}/portrelay-agent-{{VERSION}}-linux-arm64.tar.gz) |
| Windows x86_64 | [portrelay-agent-{{VERSION}}-windows-amd64.zip]({{DOWNLOAD}}/portrelay-agent-{{VERSION}}-windows-amd64.zip) |

macOS 是菜单栏 App：打开 dmg，把 PortRelay 拖进「应用程序」。App 未经 Apple 公证，首次打开请在 Finder 里右键选择「打开」。Linux / Windows 是命令行 Agent，运行后在浏览器里打开管理面板。
On macOS, open the dmg and drag PortRelay to Applications. The app is not notarized, so right-click it and choose Open the first time. On Linux and Windows, run the agent and use the web panel it opens.

### 服务端 · Server (edge gateway)

部署在同事和客户端都能连到的 Linux 机器上。
Deploy it on a Linux host that both teammates and the client can reach.

| 方式 Method | 文件 File |
| --- | --- |
| 二进制 + systemd · x86_64 | [portrelay-server-{{VERSION}}-linux-amd64.tar.gz]({{DOWNLOAD}}/portrelay-server-{{VERSION}}-linux-amd64.tar.gz) |
| 二进制 + systemd · ARM64 | [portrelay-server-{{VERSION}}-linux-arm64.tar.gz]({{DOWNLOAD}}/portrelay-server-{{VERSION}}-linux-arm64.tar.gz) |
| Docker | `{{IMAGE}}:{{VERSION}}` |

解压后运行 `sudo ./deploy/install-server.sh ./portrelay-server`，再次运行即可升级。
Unpack and run `sudo ./deploy/install-server.sh ./portrelay-server`; run it again to upgrade.

校验文件 Checksums：`SHA256SUMS`
