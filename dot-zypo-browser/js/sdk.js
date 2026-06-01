// js/sdk.js
// Unified Zypo Browser SDK for internal pages - IPC VERSION

(function() {
    let _ipc = null;
    try {
        _ipc = require('electron').ipcRenderer;
    } catch(e) {
        // Fallback for when nodeIntegration might be tricky
        console.warn('[ZypoSDK] Electron not found via require, check nodeIntegration');
    }

    const ZypoSDK = {
        _rpcPort: 0,
        _rpcToken: '',
        _translations: {},
        _lang: 'en',
        _initialized: false,
        _ipc: _ipc,

        /**
         * Initialize the SDK, load settings and translations
         */
        async init() {
            if (this._initialized) return;
            try {
                if (!this._ipc) {
                    try {
                        const electron = require('electron');
                        this._ipc = electron.ipcRenderer;
                    } catch(e) {
                        console.error('[ZypoSDK] IPC not available');
                        return;
                    }
                }

                const settings = await this._ipc.invoke('get-settings');
                this._lang = settings.language || 'en';
                this._translations = await this._ipc.invoke('get-translations', this._lang);
                
                // Fetch initial RPC details
                this._rpcPort = await this._ipc.invoke('get-rpc-port');
                this._rpcToken = await this._ipc.invoke('get-rpc-token');

                this._initialized = true;
                console.log('[ZypoSDK] Initialized (IPC Mode) for:', this._lang);
                
                window.dispatchEvent(new CustomEvent('zypo-sdk-ready'));
            } catch (e) {
                console.error('[ZypoSDK] Initialization failed:', e);
            }
        },

        /**
         * Get current Peer ID
         */
        async getPeerId() {
            if (!this._ipc) return 'unknown';
            return await this._ipc.invoke('get-last-peer-id');
        },

        /**
         * Get current RPC port
         */
        async getRpcPort() {
            if (!this._ipc) return 0;
            return await this._ipc.invoke('get-rpc-port');
        },

        async daemonFetch(path, options = {}) {
            if (!this._ipc) throw new Error('IPC not available');
            
            const result = await this._ipc.invoke('zypo-daemon-fetch', {
                path,
                method: options.method || 'GET',
                body: options.body
            });

            if (!result.success) {
                // Return a real Response object with JSON body even for errors
                return new Response(JSON.stringify({ success: false, error: result.error || 'RPC call failed' }), {
                    status: result.status || 500,
                    statusText: 'Error',
                    headers: { 'Content-Type': 'application/json' }
                });
            }

            // Return a real standards-compliant Response object
            return new Response(JSON.stringify(result.data), {
                status: 200,
                statusText: 'OK',
                headers: { 'Content-Type': 'application/json' }
            });
        },

        /**
         * Get daemon status with normalized fields
         */
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
        t(key) {
            const parts = key.split('.');
            let obj = this._translations;
            for (const p of parts) {
                if (!obj) return key;
                obj = obj[p];
            }
            return obj || key;
        },

        getTranslations() {
            return this._translations || {};
        }
    };

    window.Zypo = ZypoSDK;
})();
