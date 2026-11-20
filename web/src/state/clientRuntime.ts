type WailsRuntime = {
  environment?: { OS?: unknown }
  flags?: { vistaWailsVersion?: unknown; vistaWebViewVersion?: unknown; vistaRestartTrayOrigin?: unknown }
}

function withVersion(name: string, version: unknown) {
  return typeof version === 'string' && version.length <= 32 && /^\d+(?:\.\d+){1,3}$/.test(version) ? `${name} ${version}` : name
}

export function readClientRuntime() {
  // Read only the existing Wails configuration; no runtime calls or UA sniffing.
  const wails = (window as Window & { _wails?: WailsRuntime })._wails
  const os = wails?.environment?.OS
  const engine = os === 'linux' ? 'WebKitGTK' : os === 'windows' ? 'WebView2' : os === 'darwin' ? 'WKWebView' : null
  if (!engine) return null
  return {
    details: [withVersion('Wails', wails?.flags?.vistaWailsVersion), withVersion(engine, wails?.flags?.vistaWebViewVersion)],
    restartFromTray: os === 'windows' && wails?.flags?.vistaRestartTrayOrigin === window.location.origin,
  }
}
