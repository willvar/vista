import type { CSSProperties } from 'vue'

export interface TextAreaRef {
  focus: (options?: FocusOptions & { cursor?: 'start' | 'end' }) => void
  readonly input?: HTMLTextAreaElement
}

export interface ModalProps {
  open?: boolean
  title?: string
  width?: string | number
  footer?: null
  centered?: boolean
  closable?: boolean
  keyboard?: boolean
  maskClosable?: boolean
  destroyOnHidden?: boolean
  zIndex?: number
  rootClass?: string
  wrapClassName?: string
  okText?: string
  cancelText?: string
  confirmLoading?: boolean
  okButtonProps?: { danger?: boolean; disabled?: boolean }
  cancelButtonProps?: { disabled?: boolean }
  afterClose?: () => void
  initialFocusEl?: () => HTMLElement | null | false
  styles?: Partial<Record<'container' | 'body' | 'mask' | 'root', CSSProperties>>
}

export interface SelectOption { value: string | number; label: string; searchText?: string; fullLabel?: string; disabled?: boolean }
export interface SelectGroup { key?: string; label: string; options: SelectOption[] }
