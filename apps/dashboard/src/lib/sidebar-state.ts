// Whether the sidebar was left collapsed. The sidebar writes its state to
// the sidebar_state cookie each time it's toggled (components/ui/sidebar),
// and this reads it back when the app loads, so the choice survives a
// reload. No cookie, or one this can't read, is an open sidebar.
export function sidebarOpenAtLoad(cookie = document.cookie): boolean {
  const value = /(?:^|;\s*)sidebar_state=([^;]*)/.exec(cookie)?.[1]
  return value !== "false"
}
