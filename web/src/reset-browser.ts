import { clearVistaBrowserData } from './state/reset'

// Cookies follow the host across port changes; local storage does not. Clear
// the destination's old drafts before any application preferences are loaded.
const pending = 'vista-reset-pending=' + encodeURIComponent(window.location.origin)
if (document.cookie.split(';').some(cookie => cookie.trim() === pending)) {
  clearVistaBrowserData()
}
