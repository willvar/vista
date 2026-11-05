// Compound fields draw their border outside the native input. Make that
// padding clickable without taking focus from embedded controls or moving
// the caret when the native input itself is clicked.
export function focusInput(event: MouseEvent) {
  if (event.defaultPrevented || !(event.target instanceof Element)) return
  if (event.target.closest('input, textarea, button, a, select, label, [role="button"], [role="combobox"], [contenteditable="true"]')) return
  const input = (event.currentTarget as HTMLElement).querySelector<HTMLInputElement | HTMLTextAreaElement>('input, textarea')
  if (input && !input.disabled) input.focus()
}
