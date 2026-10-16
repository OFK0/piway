// A small client-side router: /, /about and /apps/<name> are rendered
// here, not served as files. piway's static.spa serves index.html for
// paths that do not exist, so a reload or a shared link on any of them
// works.
import { apps } from './apps.js'

const repo = 'https://github.com/OFK0/piway/tree/main/examples/'
const esc = (s) => String(s).replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' })[c])

const routes = [
  [/^\/$/, () => `<h1>Example apps</h1>
    <p>Apps that do real work, deployed by piway like your own. Each one is a directory with a piway.yaml.</p>
    <table><tr><th>App</th><th>Language</th><th>What it exercises</th></tr>
    ${apps.map((a) => `<tr><td><a href="/apps/${a.name}" data-link>${a.name}</a></td><td>${esc(a.language)}</td><td>${esc(a.what)}</td></tr>`).join('')}</table>`],
  [/^\/apps\/([a-z-]+)$/, (m) => {
    const a = apps.find((x) => x.name === m[1])
    if (!a) return null
    return `<h1>${a.name}</h1><p><strong>${esc(a.language)}.</strong> ${esc(a.what)}.</p>
      <p>Deploy it with <code>piway repo add OFK0/piway -path examples/${a.name}</code>.</p>
      <p><a href="${repo}${a.name}">Its code and README</a></p>`
  }],
  [/^\/about$/, () => `<h1>About this site</h1>
    <p>A Vite build. piway runs <code>npm ci &amp;&amp; npm run build</code> in a <code>node:24-alpine</code> container on each deploy,
    copies <code>dist</code> and serves it, switching to the new copy in one step. There is no process: nothing runs between requests.</p>
    <p>The pages are drawn in the browser; <code>static.spa</code> makes piway answer every path with <code>index.html</code>.</p>`],
]

function render() {
  for (const [re, page] of routes) {
    const m = location.pathname.match(re)
    const html = m && page(m)
    if (html) {
      document.getElementById('page').innerHTML = html
      document.title = `${document.querySelector('h1')?.textContent ?? 'piway'} · piway examples`
      return
    }
  }
  document.getElementById('page').innerHTML = '<h1>Not found</h1><p><a href="/" data-link>The apps</a></p>'
}

document.addEventListener('click', (e) => {
  const a = e.target.closest('a[data-link]')
  if (!a || e.metaKey || e.ctrlKey) return
  e.preventDefault()
  history.pushState(null, '', a.getAttribute('href'))
  render()
})
addEventListener('popstate', render)
render()

fetch('/api/info.json').then((r) => r.json()).then((info) => {
  document.getElementById('build').textContent = `Built ${info.built} with Vite ${info.vite} on Node.js ${info.node}.`
}).catch(() => {})
