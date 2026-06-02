// js/sdk.js
// Unified Zypo Browser SDK for internal pages - IPC VERSION

(function() {
    let _ipc = null;
    
    if (window.electron && window.electron.ipcRenderer) {
        _ipc = window.electron.ipcRenderer;
    } else {
        try {
            _ipc = require('electron').ipcRenderer;
        } catch(e) {}
    }

    const ZypoSDK = {
        _rpcPort: 0,
        _rpcToken: '',
        _translations: {},
        _lang: 'en',
        _initialized: false,
        _initPromise: null,
        _ipc: _ipc,

        async init() {
            if (this._initialized) return;
            if (this._initPromise) return this._initPromise;

            this._initPromise = (async () => {
                try {
                    if (!this._ipc) {
                        const electron = window.electron;
                        if (electron) this._ipc = electron.ipcRenderer;
                    }

                    if (!this._ipc) throw new Error('IPC not available');

                    const settings = await this._ipc.invoke('get-settings');
                    this._lang = settings.language || 'en';
                    this._translations = await this._ipc.invoke('get-translations', this._lang);
                    
                    this._rpcPort = await this._ipc.invoke('get-rpc-port');
                    this._rpcToken = await this._ipc.invoke('get-rpc-token');

                    this._initialized = true;
                    console.log('[ZypoSDK] Initialized for:', this._lang);
                    window.dispatchEvent(new CustomEvent('zypo-sdk-ready'));
                } catch (e) {
                    console.error('[ZypoSDK] Initialization failed:', e);
                }
            })();

            return this._initPromise;
        },

        async getPeerId() {
            if (!this._ipc) return 'unknown';
            return await this._ipc.invoke('get-last-peer-id');
        },

        async getRpcPort() {
            if (this._rpcPort) return this._rpcPort;
            if (!this._ipc) return 0;
            this._rpcPort = await this._ipc.invoke('get-rpc-port');
            return this._rpcPort;
        },

        async getRpcToken() {
            if (this._rpcToken) return this._rpcToken;
            if (!this._ipc) return '';
            this._rpcToken = await this._ipc.invoke('get-rpc-token');
            return this._rpcToken;
        },

        async daemonFetch(path, options = {}) {
            if (!this._ipc) throw new Error('IPC not available');
            
            const result = await this._ipc.invoke('zypo-daemon-fetch', {
                path,
                method: options.method || 'GET',
                body: options.body
            });

            if (!result.success) {
                return new Response(JSON.stringify({ success: false, error: result.error || 'RPC call failed' }), {
                    status: result.status || 500,
                    statusText: 'Error',
                    headers: { 'Content-Type': 'application/json' }
                });
            }

            return new Response(JSON.stringify(result.data), {
                status: 200,
                statusText: 'OK',
                headers: { 'Content-Type': 'application/json' }
            });
        },

        async getStatus() {
            try {
                const res = await this.daemonFetch('/rpc/status');
                const data = await res.json();
                if (data && !data.peer_id && data.peerId) data.peer_id = data.peerId;
                return data;
            } catch (e) {
                console.error('[ZypoSDK] Failed to fetch status:', e.message);
            }
            return null;
        },

        /**
         * Translate a key using loaded locale
         */
        t(key, translations) {
            if (!key) return '';
            const parts = key.split('.');
            let obj = translations || this._translations;
            
            if (!obj || Object.keys(obj).length === 0) return key;

            for (const p of parts) {
                if (obj && typeof obj === 'object' && p in obj) {
                    obj = obj[p];
                } else {
                    return key;
                }
            }
            return typeof obj === 'string' ? obj : key;
        },

        getTranslations() {
            return this._translations || {};
        }
    };

    window.Zypo = ZypoSDK;
})();
