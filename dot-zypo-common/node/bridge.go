package node

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

const (
	BridgeProtocolID  = "/zypo/bridge/1.0.0"
	bridgeRendezvous  = "zypo-bridge-nodes-v1"
	bridgeAnnounceKey = "/zypo/bridges"
)

// BridgeInfo describes a node acting as a bridge/relay.
type BridgeInfo struct {
	PeerID      string   `json:"peer_id"`
	Addrs       []string `json:"addrs"`       // Public multiaddrs
	Bandwidth   int64    `json:"bandwidth"`   // Mbps advertised
	BytesServed int64    `json:"bytes_served"` // Total bytes relayed (for reputation)
	Timestamp   int64    `json:"timestamp"`
}

// BridgeManager handles bridge mode: advertising this node as a bridge
// and discovering other bridges for relay path construction.
type BridgeManager struct {
	node        *ZypoNode
	enabled     bool
	mu          sync.RWMutex
	knownBridges map[peer.ID]*BridgeInfo
	bytesServed  atomic.Int64
	stopCh       chan struct{}
}

// NewBridgeManager creates a BridgeManager for the given node.
func NewBridgeManager(n *ZypoNode) *BridgeManager {
	return &BridgeManager{
		node:         n,
		knownBridges: make(map[peer.ID]*BridgeInfo),
		stopCh:       make(chan struct{}),
	}
}

// Enable activates bridge mode: registers the /zypo/bridge/1.0.0 stream handler
// and starts advertising this node as a bridge in the DHT.
func (bm *BridgeManager) Enable() {
	bm.mu.Lock()
	defer bm.mu.Unlock()

	if bm.enabled {
		return
	}
	bm.enabled = true

	// Register bridge stream handler — serves as an explicit relay endpoint
	bm.node.Host.SetStreamHandler(BridgeProtocolID, bm.handleBridgeStream)

	// Start advertising in DHT and discover other bridges
	go bm.advertiseLoop()
	go bm.discoverLoop()

	log.Printf("[Bridge] Bridge mode ENABLED on %s", bm.node.Host.ID())
}

// Disable deactivates bridge mode.
func (bm *BridgeManager) Disable() {
	bm.mu.Lock()
	defer bm.mu.Unlock()

	if !bm.enabled {
		return
	}
	bm.enabled = false
	bm.node.Host.RemoveStreamHandler(BridgeProtocolID)
	close(bm.stopCh)
	bm.stopCh = make(chan struct{}) // Reset for potential re-enable
	log.Printf("[Bridge] Bridge mode DISABLED")
}

// IsEnabled returns whether bridge mode is currently active.
func (bm *BridgeManager) IsEnabled() bool {
	bm.mu.RLock()
	defer bm.mu.RUnlock()
	return bm.enabled
}

// Status returns current bridge stats for RPC/UI.
func (bm *BridgeManager) Status() map[string]interface{} {
	bm.mu.RLock()
	defer bm.mu.RUnlock()
	bridges := make([]map[string]interface{}, 0, len(bm.knownBridges))
	for pid, info := range bm.knownBridges {
		bridges = append(bridges, map[string]interface{}{
			"peer_id":      pid.String(),
			"bytes_served": info.BytesServed,
			"bandwidth":    info.Bandwidth,
		})
	}
	return map[string]interface{}{
		"enabled":      bm.enabled,
		"bytes_served": bm.bytesServed.Load(),
		"known_bridges": bridges,
	}
}

// GetBridgeCID computes the DHT CID used for bridge rendezvous.
func GetBridgeCID() (cid.Cid, error) {
	h, err := mh.Sum([]byte(bridgeRendezvous), mh.SHA2_256, -1)
	if err != nil {
		return cid.Cid{}, err
	}
	return cid.NewCidV1(cid.Raw, h), nil
}

// advertiseLoop periodically announces this node as a bridge provider in the DHT.
func (bm *BridgeManager) advertiseLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	bm.announceInDHT()

	for {
		select {
		case <-ticker.C:
			bm.announceInDHT()
		case <-bm.stopCh:
			return
		case <-bm.node.ctx.Done():
			return
		}
	}
}

func (bm *BridgeManager) announceInDHT() {
	bridgeCID, err := GetBridgeCID()
	if err != nil {
		return
	}

	ctx, cancel := context.WithTimeout(bm.node.ctx, 30*time.Second)
	defer cancel()

	// Announce via DHT provide
	if err := bm.node.DHT.Provide(ctx, bridgeCID, true); err != nil {
		log.Printf("[Bridge] DHT announce failed: %v", err)
		return
	}

	log.Printf("[Bridge] Announced as bridge in DHT (bytes_served=%d)", bm.bytesServed.Load())
}

// discoverLoop periodically discovers other bridge nodes via DHT.
func (bm *BridgeManager) discoverLoop() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()

	// Initial discovery after a brief delay
	time.Sleep(30 * time.Second)
	bm.discoverBridges()

	for {
		select {
		case <-ticker.C:
			bm.discoverBridges()
		case <-bm.stopCh:
			return
		case <-bm.node.ctx.Done():
			return
		}
	}
}

func (bm *BridgeManager) discoverBridges() {
	bridgeCID, err := GetBridgeCID()
	if err != nil {
		return
	}

	ctx, cancel := context.WithTimeout(bm.node.ctx, 30*time.Second)
	defer cancel()

	providers, err := bm.node.DHT.FindProviders(ctx, bridgeCID)
	if err != nil {
		return
	}

	bm.mu.Lock()
	defer bm.mu.Unlock()

	for _, p := range providers {
		if p.ID == bm.node.Host.ID() {
			continue
		}
		if _, known := bm.knownBridges[p.ID]; !known {
			bm.knownBridges[p.ID] = &BridgeInfo{
				PeerID:    p.ID.String(),
				Timestamp: time.Now().Unix(),
			}
			// Add addresses to peerstore for future connections
			for _, addr := range p.Addrs {
				bm.node.Host.Peerstore().AddAddr(p.ID, addr, 2*time.Hour)
			}
			log.Printf("[Bridge] Discovered bridge node %s", p.ID)
		}
	}
}

// handleBridgeStream handles an incoming bridge protocol stream.
// This is essentially a relay: read a target peer ID from the stream,
// then pipe data between the caller and that target peer.
func (bm *BridgeManager) handleBridgeStream(s network.Stream) {
	defer s.Close()

	remotePeer := s.Conn().RemotePeer()
	log.Printf("[Bridge] Incoming bridge request from %s", remotePeer)

	// Read bridge request: JSON with target peer ID
	var req struct {
		TargetPeerID string `json:"target"`
	}
	s.SetReadDeadline(time.Now().Add(10 * time.Second))
	if err := json.NewDecoder(s).Decode(&req); err != nil {
		json.NewEncoder(s).Encode(map[string]string{"error": "invalid request"})
		return
	}
	s.SetReadDeadline(time.Time{})

	targetPID, err := peer.Decode(req.TargetPeerID)
	if err != nil || targetPID == bm.node.Host.ID() {
		json.NewEncoder(s).Encode(map[string]string{"error": "invalid target"})
		return
	}

	// Economy: check if requester has prepaid traffic
	if bm.node.EconomyManager != nil {
		if bm.node.EconomyManager.GetPrepaidTraffic(remotePeer.String()) <= 0 {
			json.NewEncoder(s).Encode(map[string]string{"error": "out_of_traffic"})
			return
		}
	}

	// Open a stream to the target peer using the Zypo transport
	ctx, cancel := context.WithTimeout(bm.node.ctx, 15*time.Second)
	defer cancel()

	targetStream, err := bm.node.Host.NewStream(ctx, targetPID, ZypoProtocolID)
	if err != nil {
		log.Printf("[Bridge] Failed to open stream to target %s: %v", targetPID, err)
		json.NewEncoder(s).Encode(map[string]string{"error": "target_unreachable"})
		return
	}
	defer targetStream.Close()

	// Acknowledge the bridge connection
	json.NewEncoder(s).Encode(map[string]string{"status": "ok"})

	log.Printf("[Bridge] Bridging %s ↔ %s", remotePeer, targetPID)

	// Bidirectional pipe: relay all data between the two streams
	done := make(chan int64, 2)
	go func() {
		n, _ := io.Copy(targetStream, s)
		done <- n
		targetStream.CloseWrite()
	}()
	go func() {
		n, _ := io.Copy(s, targetStream)
		done <- n
	}()

	// Wait for both directions to close and tally bytes
	totalBytes := <-done + <-done
	bm.bytesServed.Add(totalBytes)

	// Deduct economy credits for relayed traffic
	if bm.node.EconomyManager != nil {
		bm.node.EconomyManager.ConsumePrepaidTraffic(remotePeer.String(), totalBytes)
	}

	log.Printf("[Bridge] Bridge session closed: %s ↔ %s (%d bytes)", remotePeer, targetPID, totalBytes)
}

// GetKnownBridges returns all known bridge peer IDs.
func (bm *BridgeManager) GetKnownBridges() []peer.ID {
	bm.mu.RLock()
	defer bm.mu.RUnlock()
	bridges := make([]peer.ID, 0, len(bm.knownBridges))
	for pid := range bm.knownBridges {
		bridges = append(bridges, pid)
	}
	return bridges
}
