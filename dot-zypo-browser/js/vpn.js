/**
 * Zypo VPN - Provider & Consumer Bridge
 * Enhanced for Binary Data, Robust Networking and Real Security.
 */

class ZypoVPN {
    constructor() {
        this.pc = null;
        this.dc = null;
        this.sig = null;
        this.isSharing = false;
        this.peerID = null;
        this.clientRpcUrl = 'http://127.0.0.1:8902';
        this.rpcToken = '';
        this.signalingURL = 'ws://127.0.0.1:8905/ws';
        this.stats = { earned: 0, trafficOut: 0, clients: 0 };
        this.regInterval = null;
        this.lastTicket = null;
        this.isInitialized = false;
    }

    async init(peerID, clientRpcUrl, rpcToken, sigURL) {
        this.peerID = peerID;
        if (clientRpcUrl) this.clientRpcUrl = clientRpcUrl;
        if (rpcToken) this.rpcToken = rpcToken;
        if (sigURL) this.signalingURL = sigURL;
        this.isInitialized = true;
        console.log('[VPN] Bridge ready');
    }

    async startSharing() {
        if (!this.peerID) return;
        this.sig = new WebSocket(`${this.signalingURL}?peerID=${this.peerID}`);
        this.sig.onopen = async () => {
            this.isSharing = true;
            await this.registerAsProvider();
            this.regInterval = setInterval(() => this.registerAsProvider(), 60000);
        };
        this.sig.onmessage = async (e) => {
            const msg = JSON.parse(e.data);
            if (msg.type === 'offer') await this.handleOffer(msg);
            else if (msg.type === 'candidate') await this.handleCandidate(msg);
        };
        this.sig.onclose = () => this.stopSharing();
    }

    async stopSharing() {
        if (this.sig) this.sig.close();
        if (this.pc) this.pc.close();
        if (this.regInterval) clearInterval(this.regInterval);
        this.isSharing = false;
        this.stats.clients = 0;
    }

    async handleOffer(msg) {
        let iceServers = [
            { urls: 'stun:turn.ancial.ru:3478' }
        ];
        
        // If the backend provided specific TURN credentials, use them
        if (this.config && this.config.turnUrls && this.config.turnUsername && this.config.turnCredential) {
            iceServers.push({
                urls: this.config.turnUrls,
                username: this.config.turnUsername,
                credential: this.config.turnCredential
            });
        }

        this.pc = new RTCPeerConnection({ iceServers: iceServers });
        this.pc.ondatachannel = (e) => this.setupDataChannel(e.channel, msg.from);
        this.pc.onicecandidate = (e) => {
            if (e.candidate && this.sig.readyState === WebSocket.OPEN) {
                this.sig.send(JSON.stringify({ type: 'candidate', from: this.peerID, to: msg.from, data: e.candidate }));
            }
        };
        await this.pc.setRemoteDescription(new RTCSessionDescription(msg.data));
        const answer = await this.pc.createAnswer();
        await this.pc.setLocalDescription(answer);
        this.sig.send(JSON.stringify({ type: 'answer', from: this.peerID, to: msg.from, data: answer }));
    }

    async handleCandidate(msg) {
        if (this.pc) await this.pc.addIceCandidate(new RTCIceCandidate(msg.data));
    }

    setupDataChannel(channel, remotePeerID) {
        this.dc = channel;
        this.stats.clients++;
        this.dc.onmessage = async (e) => {
            if (e.data instanceof ArrayBuffer) {
                this.stats.trafficOut += e.data.byteLength;
                // Future: Handle raw packet relaying
            } else if (typeof e.data === 'string') {
                if (e.data.startsWith('FETCH ')) await this.processFetch(e.data.substring(6));
                else if (e.data.startsWith('TICKET ')) this.lastTicket = JSON.parse(e.data.substring(7));
            }
        };
        this.dc.onclose = () => {
            this.stats.clients = Math.max(0, this.stats.clients - 1);
            if (this.lastTicket) this.settlePayment(this.lastTicket);
        };
    }

    async processFetch(jsonStr) {
        try {
            const req = JSON.parse(jsonStr);
            if (!this.isAllowed(req.url)) {
                this.sendResponse({ id: req.id, status: 403, body: 'Blocked by Provider' });
                return;
            }

            const res = await fetch(req.url, {
                method: req.method,
                headers: req.headers,
                body: (req.method !== 'GET' && req.method !== 'HEAD') ? this.base64ToBuffer(req.body) : undefined
            });

            const blob = await res.blob();
            const buffer = await blob.arrayBuffer();
            const base64Body = this.bufferToBase64(buffer);

            this.sendResponse({
                id: req.id,
                status: res.status,
                headers: Object.fromEntries(res.headers.entries()),
                body: base64Body,
                isBase64: true
            });
            this.stats.trafficOut += buffer.byteLength;
        } catch (e) {
            this.sendResponse({ id: JSON.parse(jsonStr).id, status: 500, body: e.message });
        }
    }

    sendResponse(data) {
        if (this.dc && this.dc.readyState === 'open') {
            this.dc.send('RESPONSE ' + JSON.stringify(data));
        }
    }

    isAllowed(urlStr) {
        try {
            const u = new URL(urlStr);
            const h = u.hostname.toLowerCase();
            return !(['localhost', '127.0.0.1', '0.0.0.0'].includes(h) || h.startsWith('192.168.') || h.startsWith('10.'));
        } catch(e) { return false; }
    }

    bufferToBase64(buf) {
        return btoa(String.fromCharCode(...new Uint8Array(buf)));
    }

    base64ToBuffer(base64) {
        if (!base64) return undefined;
        return Uint8Array.from(atob(base64), c => c.charCodeAt(0));
    }

    async registerAsProvider() {
        fetch(`${this.clientRpcUrl}/rpc/vpn/register_node`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json', 'Authorization': this.rpcToken },
            body: JSON.stringify({ peer_id: this.peerID, location: 'Zypo User', flag: '🛡️', price: 0.5, bandwidth: 100 })
        }).catch(() => {});
    }

    async settlePayment(t) {
        fetch(`${this.clientRpcUrl}/rpc/vpn/settle_ticket`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json', 'Authorization': this.rpcToken },
            body: JSON.stringify(t)
        }).then(r => r.json()).then(d => { if(d.success) this.stats.earned += (t.amount_total/1000000); })
        .catch(() => {});
    }

    getStats() { return this.stats; }
}

window.zypoVPN = new ZypoVPN();
