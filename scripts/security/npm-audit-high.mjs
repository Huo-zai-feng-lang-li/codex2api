import { spawnSync } from 'node:child_process'

const allowedAdvisories = new Set([
  // This Vite admin UI uses BrowserRouter/Routes only and is built as a static
  // SPA. It does not enable React Router RSC or server action handling, and npm
  // currently has no react-router-dom release outside the affected >=7.12 <8.3
  // range. Keep all other high/critical advisories blocking.
  'GHSA-qwww-vcr4-c8h2',
])

const auditArgs = ['audit', '--omit=dev', '--audit-level=high', '--json', '--registry=https://registry.npmjs.org/']
const audit = process.platform === 'win32'
  ? spawnSync(process.env.ComSpec || 'cmd.exe', ['/d', '/s', '/c', `npm ${auditArgs.join(' ')}`], { encoding: 'utf8' })
  : spawnSync('npm', auditArgs, { encoding: 'utf8' })

if (audit.error) {
  console.error(String(audit.error))
  process.exit(1)
}

const output = audit.stdout || audit.stderr || ''
let report
try {
  report = JSON.parse(output.replace(/^\uFEFF/, ''))
} catch {
  if (output) process.stdout.write(output)
  process.exit(audit.status || 1)
}

const blocked = []
for (const vulnerability of Object.values(report.vulnerabilities || {})) {
  for (const via of vulnerability.via || []) {
    if (typeof via === 'string') continue
    const advisoryID = String(via.url || '').split('/').pop()
    if (via.severity === 'high' || via.severity === 'critical') {
      if (!allowedAdvisories.has(advisoryID)) {
        blocked.push({
          name: vulnerability.name,
          severity: via.severity,
          advisory: advisoryID,
          title: via.title,
          range: via.range,
        })
      }
    }
  }
}

if (blocked.length > 0) {
  console.error(JSON.stringify({ blocked }, null, 2))
  process.exit(1)
}

console.log(JSON.stringify({
  status: 'ok',
  allowed_advisories: [...allowedAdvisories],
  high_or_critical_blocked: 0,
}))
