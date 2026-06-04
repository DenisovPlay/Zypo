const { contextBridge, ipcRenderer, shell } = require('electron');

contextBridge.exposeInMainWorld('electron', {
  ipcRenderer: {
    invoke: (channel, ...args) => ipcRenderer.invoke(channel, ...args),
    on: (channel, func) => {
      const subscription = (event, ...args) => func(...args);
      ipcRenderer.on(channel, subscription);
      return () => ipcRenderer.removeListener(channel, subscription);
    },
    send: (channel, ...args) => ipcRenderer.send(channel, ...args)
  },
  shell: {
    openExternal: (url) => shell.openExternal(url),
    showItemInFolder: (filePath) => shell.showItemInFolder(filePath)
  },
  getPreloadPath: (name) => ipcRenderer.sendSync('get-preload-path-sync', name),
  isMac: ipcRenderer.sendSync('is-mac-sync'),
  openExternal: (url) => ipcRenderer.send('open-external', url),
  windowMinimize: () => ipcRenderer.send('window-minimize'),
  windowMaximize: () => ipcRenderer.send('window-maximize'),
  windowClose: () => ipcRenderer.send('window-close'),
});
