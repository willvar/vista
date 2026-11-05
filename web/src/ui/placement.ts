export function placement(value = 'bottomLeft'): 'top' | 'top-start' | 'top-end' | 'bottom' | 'bottom-start' | 'bottom-end' | 'left' | 'right' {
  const positions = { top: 'top', topLeft: 'top-start', topRight: 'top-end', bottom: 'bottom', bottomLeft: 'bottom-start', bottomRight: 'bottom-end', left: 'left', right: 'right' } as const
  return positions[value as keyof typeof positions] ?? 'bottom-start'
}
