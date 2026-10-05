function safePath(path: string): string | undefined {
  return (path.startsWith('/') || path === '~' || path.startsWith('~/')) && !/[\u0000-\u001f\u007f]/.test(path) ? path : undefined
}

export function oscWorkingDirectory(data: string): string | undefined {
  try {
    const url = new URL(data)
    if (url.protocol !== 'file:' || url.search || url.hash) return undefined
    return safePath(decodeURIComponent(url.pathname))
  } catch { return undefined }
}

/** Only accept a complete, idle prompt, never a command's partially echoed line. */
export function promptWorkingDirectory(line: string): string | undefined {
  const prompt = line.trimEnd()
  const ordinary = /^[^\s@]+@[^\s:]+:(\/[^#$%>]*|~(?:\/[^#$%>]*)?)[#$%>]$/.exec(prompt)
  if (ordinary) return safePath(ordinary[1]!.trimEnd())
  const bracketed = /^\[[^\s@]+@[^\s]+ (\/[^#$%>\]]*|~(?:\/[^#$%>\]]*)?)\][#$%>]$/.exec(prompt)
  return bracketed ? safePath(bracketed[1]!.trimEnd()) : undefined
}
