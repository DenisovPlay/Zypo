**English** | [Русский](README.md)

# 🌐 Zypo Mesh Network

**Zypo** is a bulletproof, next-generation decentralized P2P protocol that combines an encrypted data network, a distributed DNS system, and an autonomous P2P economy.

## 🚀 Key Features

### 1. Decentralized P2P Economy (ZPCN)
Unlike traditional systems, Zypo has no "central bank".
*   **Local Wallets:** Each device stores its own balance and transaction history locally.
*   **Ed25519 Signatures:** Every transaction is signed with the device's private key and verified locally by the recipient.
*   **Autonomy:** Payments work even if the Global Oracle is temporarily unavailable.
*   **Micropayments:** A State Channels system allows byte-by-byte payment for VPN traffic without overloading the network.

### 2. Bullet-Proof VPN & Proxy
A full P2P tunnel over libp2p with end-to-end encryption.
*   **Anti-DPI Transports:** Zypo dynamically falls back from TCP and QUIC to **WebSocket-over-TLS** to bypass Deep Packet Inspection.
*   **SOCKS5 & Forward Proxy (HTTPS):** Built-in local proxy servers (SOCKS5 on port `1080`, HTTP Proxy on `8903`). Full HTTPS (CONNECT) support is provided via a local TLS MITM.
*   **Bridge Protocol:** Nodes with open ports automatically act as Circuit Relays, helping peers behind strict NATs connect to the network.
*   **UDP Datagram Forwarding:** Full support for tunneling TCP and UDP traffic, including interception and tunneling of DNS queries (port `53`).
*   **DHT Discovery:** VPN providers are discovered via a distributed hash table (Kademlia), eliminating the need for central registries.

### 2.5 Active Network Ports
*   **8900** (TCP/QUIC) — Primary P2P listening port.
*   **8901** (TCP/WS) — Fallback WebSockets port for Anti-DPI.
*   **8903** (HTTP) — Local RPC API (Browser to Daemon communication).
*   **8999** (UDP) — Local Area Network (LAN) discovery broadcast.
*   **1080** (TCP) — Local SOCKS5 proxy port.
*   **53** (UDP) — DNS Resolution tunneling port.

### 3. Distributed DNS & Secure Hosting
*   **Zypo DNS:** Naming system for zones like `.zypo`, `.mesh`, `.rus`, etc.
*   **Oracle Verification & Mesh-Fallback:** All official domain records are signed by the Global Oracle. If the Oracle goes offline, the network falls back to a decentralized "Mesh-Only" mode using the P2P DHT to keep domains resolvable.
*   **Hidden Services (Secure Sandbox):** Host websites entirely within the network without a public IP. Strict path traversal protections ensure that the hosting directory is tightly sandboxed.

## 🛡️ Trust Model and Security

### How is the Global Registry (Oracle) authenticated?
1.  **Trust-on-First-Connect (TOFC):** Upon the first successful connection to the official bootstrap node (specified in the config), the client extracts the Ed25519 public key.
2.  **Oracle Persistence:** This key is saved in `data/oracle.pub`.
3.  **Signature Validation:** Any DNS records in the DHT claiming official status must be signed by this key.
4.  **Decentralization:** Even if the oracle node is compromised or goes offline, the network continues to function, and balances are verified entirely in a decentralized manner.

## 📂 Project Structure

*   `dot-zypo-common`: Core. P2P engine, DHT, routing, DNS logic, and secure hosting.
*   `dot-zypo-control`: Command Center (Coordinator Node). Acts as the DNS Oracle and public Relay hub.
*   `dot-zypo-client`: Client P2P daemon. Starts the TUN interface, SOCKS5 proxy, and HTTPS Forward Proxy.
*   `dot-zypo-browser`: User interface and Electron-based browser.
*   `dot-zypo-yan`: Search engine and crawler for the hidden Mesh network.
*   `dot-zypo-server`: (Deprecated) Former WebRTC signaling server. Scheduled for removal.

## 🛠️ Quick Start

### 1. Client Node
To connect to the network, you need the address of any trusted Bootstrap node. By default, the client will attempt to use built-in addresses:
```bash
go run dot-zypo-client/main.go
```
If you want to explicitly connect to the main coordination node:
```bash
go run dot-zypo-client/main.go -bootstrap /ip4/213.171.27.234/tcp/8900/p2p/12D3KooWPyuxuDGwsnBB6sKvCarjfRimWhNvvXaAhYnGkesBFbEf
```

### 2. Launch Browser
Once the client node has started and transitioned to the `Synced` state, you can open the Zypo Browser:
```bash
cd dot-zypo-browser
npm install
npm start
```

### 3. VPN Provider (Public Relay/Exit Node)
If you want to earn `ZPCN` by acting as a VPN provider, run the server node on a VPS with a public IP:
```bash
go run dot-zypo-server/main.go
```

## 📝 Documentation and Deployment
Detailed instructions on running the network, hosting your own websites, and configuring Reverse Proxy are available in the `docs/user_guide.md` file.

**Developer Notice!**
This public README covers the general operation of the network. Strictly confidential information (secret tokens, private certificate keys, internal hidden ports, and detailed security architecture) is located exclusively in the protected directory.
