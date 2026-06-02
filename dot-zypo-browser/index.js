const { app, BrowserWindow, protocol, ipcMain, dialog, session } = require('electron')
const path = require('path')
const { spawn } = require('child_process')
const fs = require('fs')
const crypto = require('crypto')

let mainWindow
let daemonProcess
let daemonHealthTimer = null
let daemonRestartCount = 0
const MAX_DAEMON_RESTARTS = 5

const net = require('net')
const RPC_TOKEN = crypto.randomBytes(16).toString('hex')
let RPC_PORT = 0 // Will be set dynamically

protocol.registerSchemesAsPrivileged([
  { scheme: 'zypo', privileges: { standard: true, secure: true, supportFetchAPI: true, bypassCSP: true } },
  { scheme: 'zb', privileges: { standard: true, secure: true, supportFetchAPI: true, bypassCSP: true } }
])

function getDaemonPath() {
  let daemonPath = path.join(__dirname, 'bin', process.platform === 'win32' ? 'daemon.exe' : 'daemon')
  if (app.isPackaged) {
    daemonPath = path.join(process.resourcesPath, 'bin', process.platform === 'win32' ? 'daemon.exe' : 'daemon')
  }
  return daemonPath
}

function getSettings() {
  const settingsFile = path.join(app.getPath('userData'), 'settings.json')
  if (fs.existsSync(settingsFile)) {
    try { return JSON.parse(fs.readFileSync(settingsFile, 'utf8')) }
    catch(e) { console.error('Failed to parse settings', e) }
  }
  return { language: 'en', startupAction: 'newtab', homePage: 'zb://newtab', commandCenterAddr: '', searchEngine: 'ya.rus', enableSearch: true }
}

function getAvailablePort() {
  return new Promise((resolve) => {
    const srv = net.createServer()
    srv.listen(0, '127.0.0.1', () => {
      const port = srv.address().port
      srv.close(() => resolve(port))
    })
  })
}

async function startDaemon() {
  const daemonPath = getDaemonPath()
  if (!fs.existsSync(daemonPath)) {
    console.error('Daemon binary not found at', daemonPath)
    return
  }

  if (RPC_PORT === 0) {
    RPC_PORT = await getAvailablePort()
  }

  // Ensure daemon working dir exists BEFORE spawning
  const daemonCwd = path.join(app.getPath('userData'), 'zypo-daemon')
  if (!fs.existsSync(daemonCwd)) {
    fs.mkdirSync(daemonCwd, { recursive: true })
  }

  const settings = getSettings()
  const args = ['-rpc-token', RPC_TOKEN, '-rpc', String(RPC_PORT), '-port', '0']

  // Pass Command Center address as bootstrap if configured (Environment takes precedence)
  const bootstrap = process.env.ZYPO_BOOTSTRAP || settings.commandCenterAddr;
  if (bootstrap && bootstrap.trim()) {
    args.push('-bootstrap', bootstrap.trim())
    console.log('Daemon starting with Command Center bootstrap:', bootstrap.trim())
  }

  console.log('Starting Zypo daemon from:', daemonPath, 'on RPC port:', RPC_PORT)
  daemonProcess = spawn(daemonPath, args, {
    stdio: 'pipe',
    cwd: daemonCwd,
    shell: false,
    windowsHide: true
  })

  daemonProcess.stdout.on('data', (data) => {
    const lines = data.toString().split('\n')
    for (let line of lines) {
      line = line.trim()
      if (!line) continue
      console.log(`[Daemon] ${line}`)
      const peerIdMatch = line.match(/^NODE_PEER_ID\s+(12D3KooW[a-zA-Z0-9]+)/)
      if (peerIdMatch) {
        LAST_PEER_ID = peerIdMatch[1]
        console.log(`[Main] Captured PeerID: ${LAST_PEER_ID}`)
      }
    }
  })
  daemonProcess.stderr.on('data', (data) => {
    console.error(`[Daemon ERR] ${data.toString().trim()}`)
  })
  daemonProcess.on('exit', (code, signal) => {
    console.log(`[Daemon] Exited with code=${code} signal=${signal}`)
    daemonProcess = null
    // Auto-restart if not intentional shutdown
    if (code !== 0 && daemonRestartCount < MAX_DAEMON_RESTARTS) {
      daemonRestartCount++
      const delay = Math.min(1000 * daemonRestartCount, 10000)
      console.log(`[Daemon] Restarting in ${delay}ms (attempt ${daemonRestartCount}/${MAX_DAEMON_RESTARTS})...`)
      setTimeout(() => startDaemon(), delay)
    } else if (daemonRestartCount >= MAX_DAEMON_RESTARTS) {
      console.error('[Daemon] Max restart attempts reached. Daemon will not be restarted.')
    }
  })
  daemonProcess.on('error', (err) => {
    console.error('[Daemon] Failed to start:', err)
  })

  // Poll until daemon responds on RPC
  console.log(`Waiting for daemon to respond on port ${RPC_PORT}...`)
  for (let i = 0; i < 60; i++) { // Increased to 60 retries (approx 12-15 seconds)
    try {
      const res = await fetch(`http://127.0.0.1:${RPC_PORT}/rpc/status`, {
        headers: { 'Authorization': RPC_TOKEN }
      })
      if (res.ok) {
        const data = await res.json();
        console.log(`Daemon is ready on port ${RPC_PORT}! (Status: ${data.economy_status})`)
        break
      }
    } catch(e) {
      // expected, daemon is booting
      if (i % 10 === 0 && i > 0) console.log(`Polling daemon... (${i}/60)`)
    }
    await new Promise(r => setTimeout(r, 250))
  }

  // Start health check loop
  startDaemonHealthCheck()
}

function stopDaemon() {
  stopDaemonHealthCheck()
  if (daemonProcess) {
    daemonProcess.kill('SIGTERM')
    daemonProcess = null
  }
}

function startDaemonHealthCheck() {
  stopDaemonHealthCheck()
  daemonHealthTimer = setInterval(async () => {
    if (!daemonProcess) return
    try {
      const http = require('http')
      await new Promise((resolve, reject) => {
        const req = http.get(`http://127.0.0.1:${RPC_PORT}/rpc/status`, { 
          timeout: 10000, 
          agent: false,
          headers: { 'Authorization': RPC_TOKEN }
        }, (res) => {
          res.on('data', () => {}); // Consume data to free memory
          res.on('end', resolve);
        })
        req.on('error', reject)
        req.on('timeout', () => { req.destroy(); reject(new Error('timeout')) })
      })
      // Daemon is healthy — reset restart counter
      if (daemonRestartCount > 0) {
        console.log('[Daemon] Health check OK, resetting restart counter')
        daemonRestartCount = 0
      }
    } catch(e) {
      console.warn('[Daemon] Health check failed:', e.message)
      // Do not kill the daemon on timeout, it might just be busy or the OS is under load.
      // If the daemon actually crashes, the 'exit' event handler will restart it.
    }
  }, 15000) // Check every 15 seconds
}

function stopDaemonHealthCheck() {
  if (daemonHealthTimer) {
    clearInterval(daemonHealthTimer)
    daemonHealthTimer = null
  }
}


function createWindow() {
  mainWindow = new BrowserWindow({
    width: 1200,
    height: 800,
    titleBarStyle: 'hiddenInset',
    backgroundColor: '#000000',
    webPreferences: {
      nodeIntegration: false,
      contextIsolation: true,
      webviewTag: true,
      preload: path.join(__dirname, 'js', 'preload_main.js')
    }
  })

  mainWindow.loadFile('index.html')
}

// Downloads history path and read
ipcMain.handle('get-downloads-path', () => path.join(app.getPath('userData'), 'downloads.json'))
ipcMain.handle('get-downloads', () => {
  const dlFile = path.join(app.getPath('userData'), 'downloads.json')
  if (fs.existsSync(dlFile)) {
    try { return JSON.parse(fs.readFileSync(dlFile, 'utf8')) } catch(e){}
  }
  return []
})

// Provide absolute app path for sandboxed preloads
ipcMain.on('get-app-path-sync', (e) => { e.returnValue = __dirname; });
ipcMain.on('get-preload-path-sync', (e, name) => { e.returnValue = path.join(__dirname, 'js', name); });

// Bookmarks logic
function getBookmarks() {
  const bookmarksFile = path.join(app.getPath('userData'), 'bookmarks.json')
  if (fs.existsSync(bookmarksFile)) {
    return JSON.parse(fs.readFileSync(bookmarksFile, 'utf8'))
  }
  return [
    { title: 'Zypo Governance', url: 'zypo://gov.zypo' },
    { title: 'Domain Registry', url: 'zypo://domains.zypo' }
  ]
}

ipcMain.handle('get-bookmarks', () => getBookmarks())
ipcMain.handle('is-bookmarked', (e, url) => getBookmarks().some(x => x.url === url))
ipcMain.handle('toggle-bookmark', (e, bookmark) => {
  const bookmarksFile = path.join(app.getPath('userData'), 'bookmarks.json')
  let list = getBookmarks()
  let existingIndex = list.findIndex(x => x.url === bookmark.url)
  let isBookmarked = false
  if (existingIndex >= 0) {
    list.splice(existingIndex, 1)
  } else {
    list.push(bookmark)
    isBookmarked = true
  }
  fs.writeFileSync(bookmarksFile, JSON.stringify(list, null, 2))
  return isBookmarked
})

// History logic
function getHistory() {
  const historyFile = path.join(app.getPath('userData'), 'history.json')
  if (fs.existsSync(historyFile)) {
    return JSON.parse(fs.readFileSync(historyFile, 'utf8'))
  }
  return []
}
ipcMain.handle('get-history', () => getHistory())
ipcMain.on('add-history', (e, item) => {
  const historyFile = path.join(app.getPath('userData'), 'history.json')
  let list = getHistory()
  list.unshift({ ...item, time: new Date().toISOString() })
  if(list.length > 1000) list = list.slice(0, 1000)
  fs.writeFileSync(historyFile, JSON.stringify(list, null, 2))
})
ipcMain.on('clear-history', () => {
  const historyFile = path.join(app.getPath('userData'), 'history.json')
  fs.writeFileSync(historyFile, JSON.stringify([]))
})

// Payment SDK
ipcMain.handle('request-payment', async (e, req) => {
  if (!RPC_PORT || RPC_PORT === 0) return { success: false, error: "Local daemon not ready" };
  const { amount, to, comment } = req;
  
  const { response } = await dialog.showMessageBox(mainWindow, {
    type: 'question',
    buttons: ['Cancel', 'Approve Payment'],
    defaultId: 1,
    cancelId: 0,
    title: 'Zypo Payment Request',
    message: `A website is requesting a payment of ${amount} ZPCN.`,
    detail: `To: ${to}\nComment: ${comment}\n\nDo you want to approve this transaction?`
  });

  if (response !== 1) return { success: false, error: "User cancelled payment" };

  try {
    // 1. Get our own PeerID
    const balRes = await fetch(`http://127.0.0.1:${RPC_PORT}/rpc?url=${encodeURIComponent('zypo://api.zypo/api/balance')}`, {
      headers: { 'Authorization': RPC_TOKEN }
    });
    const balData = await balRes.json();
    if (!balData || !balData.peer_id) throw new Error("Could not get local wallet");

    // 2. Execute Transfer
    const txBody = {
      from: balData.peer_id,
      to: to,
      amount: parseInt(amount),
      comment: comment || ''
    };
    
    const txRes = await fetch(`http://127.0.0.1:${RPC_PORT}/rpc?url=${encodeURIComponent('zypo://api.zypo/api/transfer')}`, {
      method: 'POST',
      headers: { 'Authorization': RPC_TOKEN, 'Content-Type': 'application/json' },
      body: JSON.stringify(txBody)
    });
    
    const txData = await txRes.json();
    return txData;
  } catch(err) {
    return { success: false, error: err.message };
  }
})

// Key Export/Import
ipcMain.handle('export-key', async () => {
  const daemonCwd = path.join(app.getPath('userData'), 'zypo-daemon')
  const keyPath = path.join(daemonCwd, 'client_key.bin')

  console.log('[Identity] Export requested. Checking key at:', keyPath)
  if (!fs.existsSync(keyPath)) {
    console.error('[Identity] Export failed: Key file not found.')
    return { success: false, error: 'Key file not found. Is the daemon running?' }
  }

  const { canceled, filePath } = await dialog.showSaveDialog(mainWindow, {
    title: 'Export Zypo Identity Key',
    defaultPath: 'zypo_identity_key.bin',
    filters: [{ name: 'Binary Key', extensions: ['bin'] }],
    buttonLabel: 'Export'
  })

  if (canceled || !filePath) return { success: false, canceled: true }
  try {
    fs.copyFileSync(keyPath, filePath)
    console.log('[Identity] Key exported successfully to:', filePath)
    return { success: true }
  } catch (err) {
    console.error('[Identity] Export error:', err.message)
    return { success: false, error: err.message }
  }
})

ipcMain.handle('import-key', async () => {
  console.log('[Identity] Import requested.')
  const { canceled, filePaths } = await dialog.showOpenDialog(mainWindow, {
    title: 'Import Zypo Identity Key',
    filters: [{ name: 'Binary Key', extensions: ['bin'] }],
    properties: ['openFile']
  })

  if (canceled || filePaths.length === 0) return { success: false, canceled: true }

  const sourcePath = filePaths[0]
  const daemonCwd = path.join(app.getPath('userData'), 'zypo-daemon')
  const targetPath = path.join(daemonCwd, 'client_key.bin')

  console.log('[Identity] Source file selected:', sourcePath)
  const { response } = await dialog.showMessageBox(mainWindow, {
    type: 'warning',
    buttons: ['Cancel', 'Replace Key'],
    title: 'Confirm Identity Import',
    message: 'Importing a new identity will replace your current PeerID and Wallet.',
    detail: 'The current key will be backed up as client_key.bin.bak. The daemon will restart automatically.'
  })

  if (response !== 1) {
    console.log('[Identity] Import canceled by user.')
    return { success: false }
  }

  try {
    if (fs.existsSync(targetPath)) {
        console.log('[Identity] Backing up existing key...')
        fs.copyFileSync(targetPath, targetPath + '.bak')
    }
    fs.copyFileSync(sourcePath, targetPath)

    // Restart daemon to apply new key
    console.log('[Identity] New key imported, restarting daemon...')
    daemonRestartCount = 0
    stopDaemon()
    setTimeout(() => startDaemon(), 1000)

    return { success: true }
  } catch (err) {
    console.error('[Identity] Import error:', err.message)
    return { success: false, error: err.message }
  }
})


// Settings & Localization
ipcMain.handle('get-settings', () => getSettings())
ipcMain.handle('update-settings', (e, newSettings) => {
  const settingsFile = path.join(app.getPath('userData'), 'settings.json')
  const current = getSettings()
  const updated = { ...current, ...newSettings }
  fs.writeFileSync(settingsFile, JSON.stringify(updated, null, 2))

  // If Command Center address changed, restart daemon with new bootstrap
  if (newSettings.commandCenterAddr !== undefined && newSettings.commandCenterAddr !== current.commandCenterAddr) {
    console.log('[Settings] Command Center address changed, restarting daemon...')
    daemonRestartCount = 0
    stopDaemon()
    setTimeout(() => startDaemon(), 500)
  }

  return updated
})

ipcMain.handle('get-translations', async (e, lang) => {
  try {
    const settings = getSettings();
    const targetLang = (typeof lang === 'string' ? lang : null) || settings.language || 'en';
    const localesDir = path.join(__dirname, 'locales');
    const localePath = path.join(localesDir, `${targetLang}.json`);
    
    if (fs.existsSync(localePath)) {
      const content = fs.readFileSync(localePath, 'utf8');
      return JSON.parse(content);
    }
    
    const enPath = path.join(localesDir, 'en.json');
    if (fs.existsSync(enPath)) {
      return JSON.parse(fs.readFileSync(enPath, 'utf8'));
    }
    
    console.error('[Main] Translation files not found');
    return {};
  } catch (err) {
    console.error('[Main] Failed to load translations:', err);
    return {};
  }
})

ipcMain.on('notify-settings-updated', () => {
  global.I18N_CACHE = null; // Clear cache on language change
  const { webContents } = require('electron');
  webContents.getAllWebContents().forEach(wc => {
    if (wc && !wc.isDestroyed()) {
      try {
        if (wc.mainFrame) {
          wc.send('settings-updated');
        }
      } catch(e) {}
    }
  });
})

let LAST_PEER_ID = "unknown";

ipcMain.handle('get-rpc-token', () => RPC_TOKEN)
ipcMain.handle('get-rpc-port', () => RPC_PORT)
ipcMain.handle('get-last-peer-id', () => LAST_PEER_ID)
ipcMain.on('set-last-peer-id', (e, id) => { LAST_PEER_ID = id; })

// High-level RPC fetcher to bypass renderer security/CORS
ipcMain.handle('zypo-daemon-fetch', async (e, { path, method, body }) => {
  if (!RPC_PORT) return { success: false, error: 'Daemon not ready' }
  
  try {
    const url = `http://127.0.0.1:${RPC_PORT}${path.startsWith('/') ? '' : '/'}${path}`
    const options = {
      method: method || 'GET',
      headers: { 
        'Authorization': RPC_TOKEN,
        'Content-Type': 'application/json'
      }
    }
    
    if (body) {
      if (typeof body === 'string') {
        options.body = body
      } else {
        options.body = JSON.stringify(body)
      }
    }

    const resp = await fetch(url, options)
    if (!resp.ok) {
      return { success: false, status: resp.status, error: await resp.text() }
    }
    const data = await resp.json()
    return { success: true, data }
  } catch (err) {
    return { success: false, error: err.message }
  }
})

ipcMain.on('get-rpc-port-sync', (e) => { try { e.returnValue = RPC_PORT; } catch(err){} });
ipcMain.on('get-rpc-token-sync', (e) => { try { e.returnValue = RPC_TOKEN; } catch(err){} });
ipcMain.on('get-last-peer-id-sync', (e) => { try { e.returnValue = LAST_PEER_ID; } catch(err){} });
ipcMain.on('get-language-sync', (e) => { try { e.returnValue = getSettings().language || 'en'; } catch(err){} });

ipcMain.on('log-error', (e, message) => {
  try {
    const os = require('os');
    const logPath = path.join(os.tmpdir(), 'zypo-browser-error.log');
    fs.appendFileSync(logPath, message);
  } catch(err) {
    console.error('Failed to log error:', err);
  }
});

app.whenReady().then(async () => {
RPC_PORT = await getAvailablePort()
await startDaemon()
protocol.handle('zb', async (req) => {
  const url = req.url
  const parsedUrl = new URL(url)
  const domain = parsedUrl.hostname

  if (domain === 'dist') {

       const distFile = path.join(__dirname, 'dist', parsedUrl.pathname.replace(/^\/+/, ''))
       if (fs.existsSync(distFile)) {
          const ext = path.extname(distFile)
          const mime = ext === '.css' ? 'text/css' : (ext === '.js' ? 'application/javascript' : 'text/plain')
          return new Response(fs.readFileSync(distFile), { headers: { 'Content-Type': mime } })
       }
       return new Response('Not Found', { status: 404 })
    }

    if (domain === 'js') {
       const jsFile = path.join(__dirname, 'js', parsedUrl.pathname.replace(/^\/+/, ''))
       if (fs.existsSync(jsFile)) {
          return new Response(fs.readFileSync(jsFile), { headers: { 'Content-Type': 'application/javascript' } })
       }
       return new Response('Not Found', { status: 404 })
    }

    if (domain === 'locales') {
       const localeFile = path.join(__dirname, 'locales', parsedUrl.pathname.replace(/^\/+/, ''))
       if (fs.existsSync(localeFile)) {
          return new Response(fs.readFileSync(localeFile), { headers: { 'Content-Type': 'application/json' } })
       }
       return new Response('Not Found', { status: 404 })
    }

    if (domain === 'rename-bookmark') {
       const bUrl = parsedUrl.searchParams.get('url')
       const bTitle = parsedUrl.searchParams.get('title')
       if (bUrl && bTitle) {
         const bookmarksFile = path.join(app.getPath('userData'), 'bookmarks.json')
         let list = getBookmarks()
         let target = list.find(x => x.url === bUrl)
         if (target) {
           target.title = bTitle
           fs.writeFileSync(bookmarksFile, JSON.stringify(list, null, 2))
         }
       }
       return new Response(null, { status: 302, headers: { 'Location': 'zb://newtab' } })
    }

    if (domain === 'delete-bookmark') {
       const bUrl = parsedUrl.searchParams.get('url')
       if (bUrl) {
         const bookmarksFile = path.join(app.getPath('userData'), 'bookmarks.json')
         let list = getBookmarks()
         list = list.filter(x => x.url !== bUrl)
         fs.writeFileSync(bookmarksFile, JSON.stringify(list, null, 2))
       }
       return new Response(null, { status: 302, headers: { 'Location': 'zb://newtab' } })
    }

    if (domain === 'clear-history') {
       const historyFile = path.join(app.getPath('userData'), 'history.json')
       fs.writeFileSync(historyFile, JSON.stringify([]))
       return new Response(null, { status: 302, headers: { 'Location': 'zb://history' } })
    }

    try {
      let content = fs.readFileSync(path.join(__dirname, 'pages', `${domain}.html`), 'utf8')
      if (domain === 'newtab' || domain === 'bookmarks') {
        content = content.replace('/*INJECT_BOOKMARKS*/', `window.BOOKMARKS = ${JSON.stringify(getBookmarks())};`)
      } else if (domain === 'history') {
        content = content.replace('/*INJECT_HISTORY*/', `window.HISTORY = ${JSON.stringify(getHistory())};`)
      } else if (domain === 'about') {
        const sysinfo = { 'Zypo Core': 'v1.1.0-alpha', 'Electron': process.versions.electron, 'OS Arch': process.arch, 'Platform': process.platform }
        content = content.replace('/*INJECT_SYSINFO*/', `window.SYSINFO = ${JSON.stringify(sysinfo)};`)
      } else if (domain === 'settings') {
        const settings = getSettings()
        content = content.replace('/*INJECT_SETTINGS*/', `window.SETTINGS = ${JSON.stringify(settings)};`)
      }
      return new Response(content, { headers: { 'Content-Type': 'text/html; charset=utf-8' } })
    } catch (e) { return new Response('Not Found', { status: 404 }) }
  })

  protocol.handle('zypo', async (req) => {
    const url = req.url
    const parsedUrl = new URL(url)
    const domain = parsedUrl.hostname
    // Preserve full path and query
    const fullPath = parsedUrl.pathname + parsedUrl.search
    const cleanUrl = parsedUrl.protocol + '//' + parsedUrl.hostname + fullPath

    const settings = getSettings();
    
    // Cache translations to avoid blocking disk I/O on every request
    if (!global.I18N_CACHE) global.I18N_CACHE = {};
    if (!global.I18N_CACHE[settings.language]) {
      try {
        const tPath = path.join(__dirname, 'locales', `${settings.language}.json`);
        global.I18N_CACHE[settings.language] = JSON.parse(fs.readFileSync(tPath, 'utf8'));
      } catch(e) { global.I18N_CACHE[settings.language] = {}; }
    }
    
    const i18n = global.I18N_CACHE[settings.language];

    const t = (p, def) => {
      const keys = p.split('.');
      let res = i18n;
      for (const k of keys) res = res ? res[k] : null;
      return res || def;
    };

    const renderErrorPage = (title, message, icon, url, details, retryText) => `
<!DOCTYPE html>
<html lang="${settings.language || 'en'}">
<head>
    <meta charset="UTF-8">
    <title>⚠️ ${title}</title>
    <style>
        :root {
            --bg-color: #050505;
            --card-bg: rgba(255, 255, 255, 0.03);
            --card-border: rgba(255, 255, 255, 0.08);
            --text-main: #ffffff;
            --text-muted: #a1a1aa;
        }
        * { box-sizing: border-box; }
        body {
            margin: 0; padding: 20px;
            min-height: 100vh;
            display: flex; align-items: center; justify-content: center;
            background-color: var(--bg-color);
            background-image: radial-gradient(circle at 50% 0%, rgba(239, 68, 68, 0.15) 0%, transparent 50%);
            color: var(--text-main);
            font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
        }
        .container { max-width: 560px; width: 100%; text-align: center; animation: fadeIn 0.4s ease-out; }
        @keyframes fadeIn { from { opacity: 0; transform: translateY(10px); } to { opacity: 1; transform: translateY(0); } }
        
        .icon-wrapper {
            width: 80px; height: 80px; margin: 0 auto 24px;
            background: rgba(239, 68, 68, 0.1); border: 1px solid rgba(239, 68, 68, 0.2);
            border-radius: 24px; display: flex; align-items: center; justify-content: center;
            font-size: 36px; box-shadow: 0 0 40px rgba(239, 68, 68, 0.1);
        }
        h1 { margin: 0 0 12px; font-size: 2.2rem; font-weight: 800; letter-spacing: -0.02em; }
        p.message { margin: 0 0 32px; color: var(--text-muted); font-size: 1.1rem; line-height: 1.6; }
        .card {
            background: var(--card-bg); border: 1px solid var(--card-border);
            border-radius: 24px; padding: 24px; text-align: left; margin-bottom: 32px;
            backdrop-filter: blur(20px); -webkit-backdrop-filter: blur(20px);
        }
        .label { font-size: 0.75rem; text-transform: uppercase; letter-spacing: 0.1em; color: #71717a; margin-bottom: 8px; font-weight: 700; }
        .url { font-family: ui-monospace, SFMono-Regular, monospace; color: #60a5fa; word-break: break-all; font-size: 0.95rem; line-height: 1.5; margin-bottom: 16px; padding-bottom: 16px; border-bottom: 1px solid var(--card-border); }
        .details { font-family: ui-monospace, SFMono-Regular, monospace; color: #ef4444; font-size: 0.85rem; word-break: break-all; margin: 0; }
        .btn {
            background: #ffffff; color: #000000; border: none; padding: 16px 36px;
            border-radius: 100px; font-size: 1rem; font-weight: 700; cursor: pointer;
            transition: all 0.3s cubic-bezier(0.4, 0, 0.2, 1); display: inline-flex; align-items: center; gap: 8px;
            box-shadow: 0 4px 14px rgba(255, 255, 255, 0.25);
        }
        .btn:hover { transform: translateY(-2px); box-shadow: 0 6px 20px rgba(255, 255, 255, 0.3); }
        .btn:active { transform: scale(0.95); box-shadow: 0 2px 10px rgba(255, 255, 255, 0.2); }
    </style>
</head>
<body>
    <div class="container">
        <div class="icon-wrapper">${icon}</div>
        <h1>${title}</h1>
        <p class="message">${message}</p>
        <div class="card">
            <div class="label">${t('browser.error_target_address', 'Target Address')}</div>
            <div class="url">${url}</div>
            <div class="label">${t('browser.error_details', 'Error Details')}</div>
            <div class="details">${details}</div>
        </div>
        <button class="btn" onclick="fetch('http://127.0.0.1:${RPC_PORT}/rpc/network/reconnect', {method: 'POST', headers: {'Authorization': '${RPC_TOKEN}'}}).finally(() => location.reload())">
            <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><path d="M21.5 2v6h-6M21.34 15.57a10 10 0 1 1-.92-10.44l5.08 5.08"/></svg>
            ${retryText}
        </button>
    </div>
</body>
</html>`;

    try {
      if (!RPC_PORT || RPC_PORT === 0) throw new Error("Local RPC port not ready");
      
      console.log(`[ZB Protocol] Proxying to local node (port ${RPC_PORT}): ${cleanUrl}`);

      const http = require('http');
      const proxyReq = http.request(`http://127.0.0.1:${RPC_PORT}/rpc?url=${encodeURIComponent(cleanUrl)}`, {
        method: req.method,
        headers: {
          'Authorization': RPC_TOKEN,
          ...Object.fromEntries(req.headers.entries())
        },
        timeout: 20000
      });

      if (req.signal) {
        req.signal.addEventListener('abort', () => {
          proxyReq.destroy();
        });
      }

      const responsePromise = new Promise((resolve, reject) => {
        proxyReq.on('response', (res) => {
          // If it's a success or a simple error we pass it through
          // BUT if it's a 404/503/504 and it's an HTML request, we show our pretty error
          const isHtmlRequest = req.headers.get('accept') && req.headers.get('accept').includes('text/html');
          
          if (!res.statusCode || res.statusCode >= 400) {
            if (isHtmlRequest) {
              // Read body to determine error type if possible, or just use status
              let body = '';
              res.on('data', chunk => body += chunk);
              res.on('end', () => {
                const errorType = res.statusCode;
                let title = t('browser.domain_unreachable', 'Error');
                let message = `${t('browser.error_http_status', 'HTTP Status:')} ${errorType}`;
                let icon = '⚠️';

                if (errorType === 503) {
                   title = t('browser.error_no_mesh', 'No Mesh Connectivity');
                   message = t('browser.error_no_mesh_desc', 'You are not connected to any peers.');
                   icon = '🌐';
                } else if (errorType === 504) {
                   title = t('browser.error_timeout', 'Peer Timeout');
                   message = t('browser.error_timeout_desc', 'The peer holding this domain did not respond in time.');
                   icon = '⏳';
                } else if (errorType === 502) {
                   title = t('browser.error_502_title', 'Gateway Error (502)');
                   message = t('browser.error_502_desc', 'The node holding this site is not answering or cannot be reached.');
                   icon = '⚠️';
                } else if (errorType === 404) {
                   title = t('browser.file_not_found', 'Resource Not Found');
                   message = t('browser.file_not_found_desc', 'The requested path does not exist on the target peer.');
                   icon = '🔍';
                }
                const errorHtml = renderErrorPage(title, message, icon, cleanUrl, `${t('browser.error_http_status', 'HTTP Status:')} ${errorType}`, t('browser.retry', 'Retry Connection'));
                resolve(new Response(errorHtml, { status: errorType, headers: { 'Content-Type': 'text/html; charset=utf-8' } }));
              });
              return;
            }
          }
          
          // Default: pass through the response stream
          resolve(new Response(res, { 
            status: res.statusCode, 
            headers: res.headers 
          }));
        });
        proxyReq.on('error', reject);
        proxyReq.on('timeout', () => { proxyReq.destroy(); reject(new Error("Timeout connecting to local node")); });
      });

      if (req.method !== 'GET' && req.method !== 'HEAD' && req.body) {
        // Forward body
        const reader = req.body.getReader();
        while(true) {
          const {done, value} = await reader.read();
          if (done) break;
          proxyReq.write(value);
        }
      }
      proxyReq.end();

      return await responsePromise;
    } catch (e) {
      console.error("[ZB Protocol] Fatal proxy error:", e.message);
      if (req.signal && req.signal.aborted) {
        return new Response(null, { status: 499, statusText: "Client Closed Request" });
      }
      const isHtmlRequest = req.headers.get('accept') && req.headers.get('accept').includes('text/html');
      if (!isHtmlRequest) {
        return new Response(JSON.stringify({error: e.message}), { status: 500, headers: {'Content-Type': 'application/json'} });
      }
      const title = t('browser.domain_unreachable', 'Domain Unreachable');
      const retryText = t('browser.retry', 'Retry Connection');
      const errorHtml = renderErrorPage(title, "Failed to connect to the peer or network.", "⚠️", cleanUrl, e.message, retryText);
      return new Response(errorHtml, { status: 500, headers: { 'Content-Type': 'text/html; charset=utf-8' } })
    }
  })

// Download Manager Interceptor
  session.defaultSession.on('will-download', (event, item, webContents) => {
    let askDownload = true; 
    const userSettingsFile = path.join(app.getPath('userData'), 'settings.json');
    if (fs.existsSync(userSettingsFile)) {
      try { 
        const set = JSON.parse(fs.readFileSync(userSettingsFile, 'utf8'));
        if (set.askDownload !== undefined) askDownload = set.askDownload;
      } catch(e){}
    }

    if (askDownload === false) {
      item.setSavePath(path.join(app.getPath('downloads'), item.getFilename()));
    }

    const downloadInfo = {
      id: Date.now().toString(),
      filename: item.getFilename(),
      totalBytes: item.getTotalBytes(),
      receivedBytes: 0,
      state: 'progressing',
      url: item.getURL(),
      time: new Date().toISOString()
    };
    
    let dlHistory = [];
    const dlFile = path.join(app.getPath('userData'), 'downloads.json');
    if (fs.existsSync(dlFile)) {
       try { dlHistory = JSON.parse(fs.readFileSync(dlFile, 'utf8')); } catch(e){}
    }
    dlHistory.unshift(downloadInfo);
    fs.writeFileSync(dlFile, JSON.stringify(dlHistory));
    try {
      if (mainWindow && !mainWindow.isDestroyed() && mainWindow.webContents && !mainWindow.webContents.isDestroyed() && mainWindow.webContents.mainFrame) {
        mainWindow.webContents.send('download-updated', downloadInfo);
      }
    } catch(e) {}

    item.on('updated', (event, state) => {
      downloadInfo.receivedBytes = item.getReceivedBytes();
      downloadInfo.state = state;
      try {
        if (mainWindow && !mainWindow.isDestroyed() && mainWindow.webContents && !mainWindow.webContents.isDestroyed() && mainWindow.webContents.mainFrame) {
          mainWindow.webContents.send('download-updated', downloadInfo);
        }
      } catch(e) {}
      fs.writeFileSync(dlFile, JSON.stringify(dlHistory));
    });

    item.on('done', (event, state) => {
      downloadInfo.state = state; 
      downloadInfo.path = item.getSavePath();
      try {
        if (mainWindow && !mainWindow.isDestroyed() && mainWindow.webContents && !mainWindow.webContents.isDestroyed() && mainWindow.webContents.mainFrame) {
          mainWindow.webContents.send('download-updated', downloadInfo);
        }
      } catch(e) {}
      fs.writeFileSync(dlFile, JSON.stringify(dlHistory));
    });
  });



  createWindow()
  app.on('activate', () => { if (BrowserWindow.getAllWindows().length === 0) createWindow() })
})

app.on('window-all-closed', () => {
  stopDaemon()
  if (process.platform !== 'darwin') app.quit()
})
