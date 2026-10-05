# Wails SSH 前端

Vue 3 + TypeScript + Vite，使用 xterm.js 提供真实终端渲染。

```sh
npm install
npm run dev        # 浏览器界面预览，不连接 SSH
npm run typecheck
npm run build
```

完整桌面应用需在项目根目录执行 `wails dev`。`src/api.ts` 封装 Wails Go 方法和事件；`TerminalPane.vue` 为每个 SSH 会话维护独立终端。

凭据与主机指纹确认、SFTP 传输和原生文件选择器均由 Go 后端处理。浏览器预览不会模拟服务器或传输结果。桌面构建与使用说明见项目根目录 README。
