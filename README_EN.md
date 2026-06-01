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
A full P2P tunnel over WebRTC with end-to-end encryption.
*   **SOCKS5 & Forward Proxy:** Built-in local proxy servers (SOCKS5 on port `8910`, HTTP Proxy on `8903`) for reliable traffic routing without root privileges.
*   **Optional TUN (Root):** With administrator privileges, you can launch a virtual network interface (e.g., `utun10` or `zypo-tun0`) to transparently intercept and route all OS TCP traffic through the built-in gVisor TCP/IP stack.
*   **DHT Discovery:** VPN providers are discovered via a distributed hash table (Kademlia), eliminating the need for central registries.

### 3. Distributed DNS & Hosting
*   **Zypo DNS:** Naming system for zones like `.zypo`, `.mesh`, `.rus`, etc.
*   **Oracle Verification:** All official domain records are signed by the Global Oracle. Nodes automatically fetch the oracle's public key upon first connecting to a bootstrap node and verify signatures for all DNS responses.
*   **Hidden Services:** Host websites entirely within the network without a public IP.

## 🛡️ Trust Model and Security

### How is the Global Registry (Oracle) authenticated?
1.  **Trust-on-First-Connect (TOFC):** Upon the first successful connection to the official bootstrap node (specified in the config), the client extracts the Ed25519 public key from the Noise handshake.
2.  **Oracle Persistence:** This key is saved in `data/oracle.pub`.
3.  **Signature Validation:** Any DNS records in the DHT claiming official status must be signed by this key. If the signature is invalid, the record is ignored.
4.  **Decentralization:** Even if the oracle node is compromised, attackers cannot steal user funds, as balances are stored and verified entirely in a decentralized manner.

## 📂 Project Structure

*   `dot-zypo-common`: Protocol core, P2P engine, economy, and cryptography.
*   `dot-zypo-client`: Client P2P node (local proxy, TUN, SOCKS5, wallet RPC server).
*   `dot-zypo-server`: WebRTC signaling server and public Relay node.
*   `dot-zypo-browser`: User interface and Electron-based browser.
*   `dot-zypo-yan`: Search engine and crawler for the hidden Mesh network.

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
