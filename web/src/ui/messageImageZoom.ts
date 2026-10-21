import mediumZoom, { type Zoom } from 'medium-zoom'

// medium-zoom installs document listeners and has no destroy method. Share one
// instance across messages, including rows mounted by the virtual message list.
let zoom: Zoom | undefined
export function messageImageZoom() {
  return zoom ||= mediumZoom({ margin: 24, background: 'rgba(7, 12, 21, .88)', scrollOffset: 40 })
}
