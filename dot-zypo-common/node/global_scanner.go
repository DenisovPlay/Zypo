package node

import (
	"bufio"
	"context"
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

const GlobalCCDiscoveryTag = "zypo-command-center-v1"

// Public IPFS bootstrappers used as a bridge to find Zypo CC via DHT
var publicIPFSBootstrappers = []string{
	"/dnsaddr/bootstrap.libp2p.io/p2p/QmNnooDuMpbfRAsEqJZsM3TzRVz5T1NDN4F2J5c581q4Yt",
	"/dnsaddr/bootstrap.libp2p.io/p2p/QmQCU2EcMqAqQPR2i9bChDtGNJchTbq5TbXBPxY8macTZX",
	"/ip4/104.131.131.82/tcp/4001/p2p/QmaCpDMGvV2BGHeYERUEnRQAwe3N8SzbUtfsmvsqQLuvuJ",
}

func getCCRendezvousCid() cid.Cid {
	c, _ := cid.Parse(CC_RENDEZVOUS_CID)
	return c
}

// StartGlobalScanner handles dynamic Command Center discovery without hardcoded IPs.
// It uses three strategies:
// 1. LAN broadcast (via lan_scanner.go)
// 2. Global DHT Rendezvous (using CC_RENDEZVOUS_CID)
// 3. P2P Gossip (nodes share CC location with each other)
func (n *ZypoNode) StartGlobalScanner() {
	if n.cfg.IsCommandCenter {
		go n.announceCCPresence()
		return
	}
	go n.huntForCommandCenter()
	go n.startGossipLoop()
}

func (n *ZypoNode) announceCCPresence() {
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()

	for {
		ctx, cancel := context.WithTimeout(n.ctx, 5*time.Minute)
		log.Printf("[Global DHT] Announcing Command Center presence on public Rendezvous...")
		err := n.DHT.Provide(ctx, getCCRendezvousCid(), true)
		if err != nil {
			log.Printf("[Global DHT] Failed to announce CC: %v", err)
		}
		cancel()

		select {
		case <-n.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (n *ZypoNode) huntForCommandCenter() {
	// Only hunt if we don't have a direct connection to a verified CC
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	// Initial hunt
	n.findCCInDHT()

	for {
		select {
		case <-n.ctx.Done():
			return
		case <-ticker.C:
			if !n.hasHealthyCC() {
				log.Println("[Global DHT] CC connection lost or not found, initiating global search...")
				n.connectToPublicBootstrappers()
				n.findCCInDHT()
			}
		}
	}
}

func (n *ZypoNode) hasHealthyCC() bool {
	n.bootstrapMu.Lock()
	defer n.bootstrapMu.Unlock()
	for _, bid := range n.BootstrapIDs {
		if n.Host.Network().Connectedness(bid) == network.Connected {
			if lastSeen, ok := n.bootstrapHealth[bid]; ok && time.Since(lastSeen) < 5*time.Minute {
				return true
			}
		}
	}
	return false
}

func (n *ZypoNode) connectToPublicBootstrappers() {
	var wg sync.WaitGroup
	for _, maddrStr := range publicIPFSBootstrappers {
		maddr, err := multiaddr.NewMultiaddr(maddrStr)
		if err != nil {
			continue
		}
		info, err := peer.AddrInfoFromP2pAddr(maddr)
		if err != nil {
			continue
		}

		wg.Add(1)
		go func(pi peer.AddrInfo) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(n.ctx, 10*time.Second)
			defer cancel()
			n.Host.Connect(ctx, pi)
		}(*info)
	}
	wg.Wait()
}

func (n *ZypoNode) findCCInDHT() {
	ctx, cancel := context.WithTimeout(n.ctx, 2*time.Minute)
	defer cancel()

	log.Printf("[Global DHT] Hunting for Command Center globally via Rendezvous...")
	providers, err := n.DHT.FindProviders(ctx, getCCRendezvousCid())
	if err != nil {
		return
	}

	for _, p := range providers {
		if p.ID == n.Host.ID() {
			continue
		}
		log.Printf("[Global DHT] Discovered potential CC: %s", p.ID)
		if n.connectToBootstrapInfo(&p) != nil {
			log.Printf("[Global DHT] Successfully discovered and linked to Command Center.")
			return
		}
	}
}

// startGossipLoop periodically asks peers if they know where the Command Center is.
func (n *ZypoNode) startGossipLoop() {
	ticker := time.NewTicker(3 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-n.ctx.Done():
			return
		case <-ticker.C:
			if !n.hasHealthyCC() {
				n.askPeersForCC()
			}
		}
	}
}

// askPeersForCC asks each connected peer individually for their known CC peers.
// Unlike findCCInDHT (which queries our own DHT view), this directly gossips
// with neighbors to discover CC addresses they know about.
func (n *ZypoNode) askPeersForCC() {
	peers := n.Host.Network().Peers()
	for _, p := range peers {
		go func(pid peer.ID) {
			if n.hasHealthyCC() {
				return // Found one already
			}
			ctx, cancel := context.WithTimeout(n.ctx, 8*time.Second)
			defer cancel()

			s, err := n.Host.NewStream(ctx, pid, ZypoProtocolID)
			if err != nil {
				return
			}
			defer s.Close()

			// Send gossip request — ask peer for their CC bootstrap info
			reqBytes, _ := json.Marshal(ZypoRequest{Action: "cc_gossip"})
			s.SetWriteDeadline(time.Now().Add(3 * time.Second))
			s.Write(append(reqBytes, '\n'))

			// Read response header
			reader := bufio.NewReader(s)
			s.SetReadDeadline(time.Now().Add(5 * time.Second))
			hLine, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			var header ZypoHeader
			if json.Unmarshal([]byte(hLine), &header) != nil || header.Status != 200 {
				return
			}
			// Read body (list of CC multiaddrs)
			bodyLine, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			var ccAddrs []string
			if json.Unmarshal([]byte(bodyLine), &ccAddrs) != nil {
				return
			}
			for _, addr := range ccAddrs {
				maddr, err := multiaddr.NewMultiaddr(addr)
				if err != nil {
					continue
				}
				info, err := peer.AddrInfoFromP2pAddr(maddr)
				if err != nil || info.ID == n.Host.ID() {
					continue
				}
				log.Printf("[Gossip] Peer %s told us about CC candidate %s", pid, info.ID)
				if n.connectToBootstrapInfo(info) != nil {
					log.Printf("[Gossip] Successfully connected to CC via peer %s", pid)
					return
				}
			}
		}(p)
	}
}

func (n *ZypoNode) connectToBootstrapInfo(info *peer.AddrInfo) *peer.AddrInfo {
	ctx, cancel := context.WithTimeout(n.ctx, 15*time.Second)
	defer cancel()

	if err := n.Host.Connect(ctx, *info); err != nil {
		return nil
	}

	log.Printf("[Mesh] Connected to discovered Command Center: %s", info.ID)

	n.bootstrapMu.Lock()
	exists := false
	for _, id := range n.BootstrapIDs {
		if id == info.ID {
			exists = true
			break
		}
	}
	if !exists {
		n.BootstrapIDs = append(n.BootstrapIDs, info.ID)
	}
	n.bootstrapHealth[info.ID] = time.Now()
	n.bootstrapMu.Unlock()

	n.resolveOraclePubKey(info.ID)

	select {
	case n.dhtRefreshTrigger <- struct{}{}:
	default:
	}

	return info
}
