// js/preload.js
try {
    const ipc = require('electron').ipcRenderer;
    const rpcPort = ipc.sendSync('get-rpc-port-sync');
    const rpcToken = ipc.sendSync('get-rpc-token-sync');
    const peerId = ipc.sendSync('get-last-peer-id-sync');
    const language = ipc.sendSync('get-language-sync');

    const zypoPayApi = {
      requestPayment: (amount, to, comment) => {
        return ipc.invoke('request-payment', { amount, to, comment });
      }
    };

    const zypoApi = {
        rpcPort: rpcPort,
        rpcToken: rpcToken,
        language: language,
        getPeerId: async () => { return ipc.sendSync('get-last-peer-id-sync'); },
        // Simple property fallback since getters don't cross contextBridge well
        peerId: peerId
    };

    if (process.contextIsolated) {
        const { contextBridge } = require('electron');
        contextBridge.exposeInMainWorld('zypoPay', zypoPayApi);
        contextBridge.exposeInMainWorld('zypo', zypoApi);
        contextBridge.exposeInMainWorld('ZYPO_RPC_PORT', rpcPort);
    } else {
        window.zypoPay = zypoPayApi;
        window.zypo = { ...zypoApi };
        Object.defineProperty(window.zypo, 'peerId', {
            get: () => ipc.sendSync('get-last-peer-id-sync')
        });
        window.ZYPO_RPC_PORT = rpcPort;
    }
    
    window.dispatchEvent(new CustomEvent('zypo-ready', { detail: { peerId: peerId, language: language } }));
} catch (e) {
    console.error("Zypo Preload Error:", e);
}
window.addEventListener('zypo-bridge', (e) => {
    const { channel, args } = e.detail;
    require('electron').ipcRenderer.sendToHost(channel, ...args);
});
