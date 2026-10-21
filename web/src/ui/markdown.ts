import DOMPurify from 'dompurify'
import MarkdownIt from 'markdown-it'

const markdown = new MarkdownIt({ html: false, linkify: true, breaks: true })
markdown.renderer.rules.link_open = (tokens, index, options, _env, renderer) => {
  const link = tokens[index]!
  const href = link.attrGet('href')
  if (href && !String(href).startsWith('#')) {
    link.attrSet('target', '_blank')
    link.attrSet('rel', 'noopener noreferrer')
  }
  return renderer.renderToken(tokens, index, options)
}

export function renderMarkdown(content: string) {
  return DOMPurify.sanitize(markdown.render(content), { ADD_ATTR: ['target'] })
}
