// Markdown → sicheres HTML (DOMPurify). Code-Blöcke bekommen Kopfzeile + Kopieren-Button.
import { marked, Renderer } from 'marked'
import DOMPurify from 'dompurify'

const esc = (s: string) => s.replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' })[c]!)
const r = new Renderer()
r.code = ({ text, lang }) => {
  const l = (lang || '').split(/\s/)[0]
  return `<div class="md-code"><div class="md-code-head"><span>${esc(l || 'text')}</span><button type="button" class="md-copy" data-copy>Kopieren</button></div><pre><code>${esc(text)}</code></pre></div>`
}
r.link = ({ href, text }) => `<a href="${esc(href || '')}" target="_blank" rel="noopener noreferrer nofollow">${text}</a>`
marked.setOptions({ gfm: true, breaks: true })

export function renderMarkdown(src: string): string {
  const html = marked.parse(src || '', { renderer: r, async: false }) as string
  return DOMPurify.sanitize(html, { ADD_ATTR: ['target', 'data-copy'], FORBID_TAGS: ['style', 'iframe', 'form', 'input'], FORBID_ATTR: ['style'] })
}
