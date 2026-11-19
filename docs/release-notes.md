云桥是基于 Wails 的 SSH / SFTP 桌面客户端，支持多标签终端、连接管理、文件上传下载和命令历史。

## 下载与安装

| 文件 | 适用电脑 | 安装方式 |
| --- | --- | --- |
| `wails-ssh-windows-amd64-setup.exe` | Windows 10/11，Intel / AMD 64 位 | 运行安装程序；自动检查并按需安装 WebView2（需要网络） |
| `wails-ssh-windows-amd64-portable.zip` | Windows 10/11，Intel / AMD 64 位 | 解压后运行 `wails-ssh.exe`；需要已安装 Microsoft Edge WebView2 Runtime |
| `wails-ssh-macos-arm64.zip` | Apple Silicon Mac（M 系列），macOS 12+ | 解压，将 `wails-ssh.app` 拖入“应用程序” |
| `wails-ssh-macos-amd64.zip` | Intel Mac，macOS 12+ | 解压，将 `wails-ssh.app` 拖入“应用程序” |

`SHA256SUMS.txt` 提供下载文件的 SHA-256 校验值。GitHub 自动附带的 Source code 是源码，不能直接运行。

## 首次打开

本版本未使用 Apple Developer ID / Windows 代码签名证书，也未进行 Apple 公证，系统可能提示未知发布者。请确认下载来自本仓库：

- macOS：首次打开被阻止时，在“系统设置 → 隐私与安全性”中选择“仍要打开”，按系统提示确认。不要全局关闭 Gatekeeper。
- Windows：SmartScreen 可能提示未知应用；核对来源与校验值后，按系统提供的选项继续。

连接和凭据保存在本机 `WailsSSH/servers.db`，密码与口令为 SQLite 明文；不要分享该数据库。更新应用不会主动删除现有连接数据。
