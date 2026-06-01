package node

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
)

// --- DNS & DHT LOGIC (CRYPTO SECURITY) ---

// LocalDNSOverride holds a locally-known peer ID for a domain,
// bypassing DHT lookup. Set by nodes that host domains themselves.
type LocalDNSOverride struct {
	PeerID peer.ID
}

// AddLocalDNSOverride registers a domain→peerID mapping that is
// resolved before hitting the DHT. Call this when a node starts
// hosting a domain locally.
func (n *ZypoNode) AddLocalDNSOverride(domain string, pid peer.ID) {
	n.localDNSMu.Lock()
	defer n.localDNSMu.Unlock()
	n.localDNSOverrides[domain] = LocalDNSOverride{PeerID: pid}
	log.Printf("[DNS] Local override registered: %s → %s", domain, pid)
	n.persistDNSOverrides()
}

func (n *ZypoNode) loadDNSOverrides() {
	b, err := os.ReadFile(filepath.Join(n.cfg.DataDir, "dns_overrides.json"))
	if err != nil {
		return
	}
	var stored map[string]string
	if err := json.Unmarshal(b, &stored); err != nil {
		return
	}
	n.localDNSMu.Lock()
	defer n.localDNSMu.Unlock()
	for domain, pidStr := range stored {
		pid, err := peer.Decode(pidStr)
		if err == nil {
			n.localDNSOverrides[domain] = LocalDNSOverride{PeerID: pid}
		}
	}
	log.Printf("[DNS] Loaded %d local overrides from disk", len(n.localDNSOverrides))
}

func (n *ZypoNode) persistDNSOverrides() {
	n.localDNSMu.RLock()
	defer n.localDNSMu.RUnlock()
	stored := make(map[string]string)
	for domain, override := range n.localDNSOverrides {
		stored[domain] = override.PeerID.String()
	}
	b, _ := json.MarshalIndent(stored, "", "  ")
	os.MkdirAll(n.cfg.DataDir, 0755)
	os.WriteFile(filepath.Join(n.cfg.DataDir, "dns_overrides.json"), b, 0644)
}

func (n *ZypoNode) RegisterDomainDHT(record *ZypoRecord) error {
	val, err := json.Marshal(record)
	if err != nil {
		return err
	}

	key := "/zypo/dns/" + record.Domain
	log.Printf("[DNS] Scheduling background DHT publication for %s", record.Domain)
	
	// Make it non-blocking
	go func() {
		// Wait for DHT to be initialized
		for i := 0; i < 10; i++ {
			if n.DHT != nil {
				break
			}
			time.Sleep(1 * time.Second)
		}
		
		if n.DHT == nil {
			log.Printf("[DNS] Cannot publish %s: DHT not initialized", record.Domain)
			return
		}

		// Повторяем попытки публикации бесконечно, пока не появятся DHT-серверы в сети
		attempt := 1
		for {
			ctx, cancel := context.WithTimeout(n.ctx, 30*time.Second)
			err := n.DHT.PutValue(ctx, key, val)
			cancel()
			
			if err == nil {
				log.Printf("[DNS] Successfully published %s to DHT in background", record.Domain)
				return
			}
			
			// Если сеть состоит только из этой ноды, PutValue возвращает ошибку отсутствия пиров,
			// НО локально запись уже сохранена. Считаем это успехом.
			if strings.Contains(err.Error(), "failed to find any peer in table") || strings.Contains(err.Error(), "routing: not found") {
				log.Printf("[DNS] Warn: Published %s locally, but no other DHT servers in network to replicate to", record.Domain)
				return
			}
			
			log.Printf("[DNS] Background DHT publication failed for %s (attempt %d): %v", record.Domain, attempt, err)
			
			// Если нода выключается, прерываем цикл
			select {
			case <-n.ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			attempt++
		}
	}()
	
	return nil
}

func (n *ZypoNode) ResolveDomain(domain string) ([]peer.ID, error) {
	// 0. Check local overrides first — instant, no network required.
	n.localDNSMu.RLock()
	if override, ok := n.localDNSOverrides[domain]; ok {
		n.localDNSMu.RUnlock()
		return []peer.ID{override.PeerID}, nil
	}
	n.localDNSMu.RUnlock()

	// 1. Try DHT with retries (routing table might be warming up).
	key := "/zypo/dns/" + domain
	
	var val []byte
	var err error
	maxResolveRetries := 5
	for i := 0; i < maxResolveRetries; i++ {
		ctx, cancel := context.WithTimeout(n.ctx, 10*time.Second) // 10 second timeout for DHT resolution
		val, err = n.DHT.GetValue(ctx, key)
		cancel()
		
		if err == nil {
			break
		}
		
		// Если ошибка "routing: not found", DHT возможно еще загружается, подождем и попробуем еще раз
		if strings.Contains(err.Error(), "routing: not found") || strings.Contains(err.Error(), "failed to find any peer") {
			log.Printf("[DNS] DHT not ready for %s, retrying (%d/%d)...", domain, i+1, maxResolveRetries)
			time.Sleep(1 * time.Second)
			continue
		}
		break // Если другая ошибка (например, таймаут), прерываем
	}
	
	if err == nil {
		var record ZypoRecord
		if err := json.Unmarshal(val, &record); err == nil {
			var pids []peer.ID
			for _, h := range record.HostIDs {
				pid, err := peer.Decode(h)
				if err == nil {
					pids = append(pids, pid)
				}
			}
			if len(pids) > 0 {
				// Populate the peerstore with addresses for each resolved host.
				// DHT returns the peer ID but not its addresses; FindPeer does a
				// Kademlia lookup to discover where the peer is actually listening,
				// so we can dial it immediately without a "no addresses" error.
				for _, pid := range pids {
					if len(n.Host.Peerstore().Addrs(pid)) > 0 {
						// Already have addresses — skip the FindPeer round-trip.
						continue
					}
					findCtx, findCancel := context.WithTimeout(n.ctx, 8*time.Second)
					addrInfo, findErr := n.DHT.FindPeer(findCtx, pid)
					findCancel()
					if findErr == nil && len(addrInfo.Addrs) > 0 {
						n.Host.Peerstore().AddAddrs(addrInfo.ID, addrInfo.Addrs, peerstore.TempAddrTTL)
						log.Printf("[DNS] Discovered %d addresses for host %s via DHT", len(addrInfo.Addrs), pid)
					} else {
						log.Printf("[DNS] FindPeer for %s: %v (will try dialing anyway)", pid, findErr)
					}
				}
				return pids, nil
			}
		}
	} else {
		log.Printf("[DNS] DHT GetValue failed for %s: %v", domain, err)
	}

	// 2. DHT failed. If we are connected to a verified Command Center, try routing 
	// the request there as a fallback. The CC acts as the ultimate source of truth
	// for all signed records in the network.
	peers := n.Host.Network().Peers()
	if len(peers) == 0 {
		return nil, fmt.Errorf("domain %s not found in DHT and no connected peers", domain)
	}

	// 3. Use verified BootstrapIDs first (explicitly connected Command Centers).
	var connectedBootstraps []peer.ID
	n.bootstrapMu.Lock()
	for _, bid := range n.BootstrapIDs {
		for _, p := range peers {
			if p == bid {
				connectedBootstraps = append(connectedBootstraps, p)
			}
		}
	}
	n.bootstrapMu.Unlock()

	if len(connectedBootstraps) > 0 {
		log.Printf("[DNS] Fallback: Routing %s to verified bootstrap", domain)
		return connectedBootstraps, nil
	}

	return nil, fmt.Errorf("[DNS] domain %s not found in DHT and Command Center is not directly connected", domain)
}
