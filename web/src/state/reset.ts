// Leave other applications' browser data intact, including on shared origins.
export function clearVistaBrowserData() {
  for (const getStorage of [() => localStorage, () => sessionStorage]) {
    try {
      const storage = getStorage()
      const keys = Array.from({ length: storage.length }, (_, index) => storage.key(index))
      for (const key of keys) if (key?.startsWith('vista-')) storage.removeItem(key)
    } catch { /* Storage may be disabled by browser policy. */ }
  }
  for (const cookie of document.cookie.split(';')) {
    const name = cookie.trim().split('=')[0] || ''
    if (name.startsWith('vista-')) document.cookie = `${name}=; Path=/; Max-Age=0; SameSite=Strict`
  }
}
