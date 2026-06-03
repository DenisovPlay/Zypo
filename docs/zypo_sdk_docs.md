# Zypo Web Developer SDK

Welcome to the Zypo Web Developer documentation! Building a site or application for the Zypo decentralized network is similar to standard web development, but with powerful native Web3 capabilities injected directly into the browser.

When users visit your `zypo://` application, the Zypo Browser automatically exposes a secure JavaScript SDK.

---

## 1. Frontend Authentication (Client-Side SDK)

Every user in the Zypo network is uniquely identified by their **PeerID** (an Ed25519 cryptographic public key hash, e.g., `12D3KooW...`).

You can access the current visitor's PeerID natively in Javascript without any login forms:

```javascript
// Wait for the Zypo SDK to be injected
window.addEventListener('zypo-ready', () => {
    // Synchronous access
    const peerId = window.zypo.peerId;
    console.log("Logged in as:", peerId);

    // Or asynchronous access
    window.zypo.getPeerId().then(id => {
        console.log("Logged in as:", id);
    });
});
```

> [!TIP]
> Use `window.zypo.language` to get the user's localized language code (e.g. `en`, `ru`) and serve localized content automatically!

---

## 2. Backend Authentication (Server-Side)

If you are building a dynamic site (like a **Next.js** or **Express.js** application) and hosting it using `dot-zypo-server`'s Reverse Proxy feature, you need a secure way to know who is calling your API.

Since client-side Javascript can be spoofed, **do not trust PeerIDs sent in the POST body**. 

Instead, `dot-zypo-server` authenticates the encrypted P2P stream at the network level and securely injects the verified PeerID into your HTTP request headers before proxying it to your local app.

Read the `X-Zypo-Peer-Id` header in your backend:

```javascript
// Next.js API Route Example (app/api/hello/route.ts)
import { NextResponse } from 'next/server';

export async function GET(request) {
    // Securely provided by the Zypo Node proxy
    const peerId = request.headers.get('x-zypo-peer-id');
    const isZypoNetwork = request.headers.get('x-zypo-network') === 'true';

    if (!peerId) {
        return NextResponse.json({ error: "Unauthorized" }, { status: 401 });
    }

    return NextResponse.json({ message: `Hello, ${peerId}!` });
}
```

> [!IMPORTANT]
> The `X-Zypo-Peer-Id` header is cryptographically guaranteed by the underlying libp2p connection. Only `dot-zypo-server` can set it when proxying requests from the mesh.

---

## 3. Native Payments (Apple Pay Style)

Zypo supports seamless native payments using ZPCN. You don't need to ask the user to sign complex transactions or copy-paste addresses.

Use `window.zypoPay.requestPayment(amount, to, comment)` to trigger a native payment modal in the user's browser.

### Example: E-Commerce Checkout
```javascript
async function buyItem() {
    try {
        const amount = 50; // ZPCN
        const destinationPeer = "12D3KooWYourStorePeerId...";
        const memo = "Order #12345";

        // This pauses execution and opens the native Zypo Browser modal
        // The user will see: "A website is requesting a payment of 50 ZPCN."
        const txResult = await window.zypoPay.requestPayment(amount, destinationPeer, memo);

        if (txResult.success) {
            alert(`Payment successful! Transaction hash: ${txResult.data.tx_hash}`);
            // Inform your backend to verify the transaction hash
        } else {
            console.error("Payment failed or cancelled:", txResult.error);
        }
    } catch (err) {
        console.error("SDK Error:", err);
    }
}
```

### Verifying Payments on the Backend

When the frontend sends the `tx_hash` to your backend, you must verify it actually exists on the network before fulfilling the order. 

To verify a transaction, your backend (Next.js/Express) should query the local `dot-zypo-server` node RPC at `http://127.0.0.1:8901/rpc/network/tx?hash=<hash>`.

---

## 4. Setting Up a Dynamic Site (`reverse_proxy.json`)

To serve a standard Next.js, React, or Python application to the Zypo network:

1. Run your web app locally (e.g., `http://127.0.0.1:3000`).
2. Inside your Zypo node's `zypo_sites` folder, create a directory for your domain (e.g. `zypo_sites/myapp.zypo`).
3. Create a `reverse_proxy.json` file in `zypo_sites`:

```json
{
    "myapp.zypo": "http://127.0.0.1:3000"
}
```

4. Start your `dot-zypo-server`. 
Now, anyone in the Zypo mesh visiting `zypo://myapp.zypo` will securely tunnel into your local Next.js server, with their `X-Zypo-Peer-Id` automatically provided in the headers!
