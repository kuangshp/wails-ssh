/** Resolve the current Wails object on each call; previews and older bundles may lack it. */
export function createDesktopBridge<Backend extends object>(resolveBackend: () => object | null | undefined): Backend {
  return new Proxy({} as Backend, {
    get: (_target, key) => {
      // The bridge is an API object, not a promise or a symbol-based protocol.
      if (typeof key !== 'string' || key === 'then') return undefined
      return async (...args: unknown[]) => {
        const backend = resolveBackend()
        if (!backend) throw new Error('请在 云桥桌面应用中使用此功能。浏览器仅提供界面预览。')
        const method: unknown = Reflect.get(backend, key)
        if (typeof method !== 'function') {
          throw new Error(`当前云桥版本与页面不匹配，缺少接口 ${key}。请退出应用并重新打开；仍有问题时请重新安装最新版本。`)
        }
        // Preserve the receiver, values, and backend errors. Never include arguments
        // in diagnostics: some methods receive passwords or command contents.
        return Reflect.apply(method, backend, args)
      }
    },
  })
}
