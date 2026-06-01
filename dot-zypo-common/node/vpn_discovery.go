package node

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/multiformats/go-multihash"
)

type VPNAnnouncement struct {
	PeerID    string  `json:"peer_id"`
	Location  string  `json:"location"`
	Flag      string  `json:"flag"`
	Price     float64 `json:"price"`
	Bandwidth int     `json:"bandwidth"`
	Timestamp int64   `json:"timestamp"`
	Signature []byte  `json:"signature"`
}

func getVpnCid() cid.Cid {
	pref := cid.Prefix{
		Version:  1,
		Codec:    cid.Raw,
		MhType:   multihash.SHA2_256,
		MhLength: -1,
	}
	c, _ := pref.Sum([]byte("/zypo/vpn-nodes"))
	return c
}

func (n *ZypoNode) RegisterVPNNodeDHT(ann *VPNAnnouncement) error {
	msg := []byte(fmt.Sprintf("%s|%s|%f|%d", ann.PeerID, ann.Location, ann.Price, ann.Timestamp))
	sig, err := n.PrivKey.Sign(msg)
	if err != nil {
		return err
	}
	ann.Signature = sig

	val, err := json.Marshal(ann)
	if err != nil {
		return err
	}

	key := "/zypo/vpn/" + ann.PeerID
	log.Printf("[VPN] Publishing VPN announcement to DHT for %s", ann.PeerID)
	return n.DHT.PutValue(n.ctx, key, val)
}

func (n *ZypoNode) DiscoverVPNNodes() ([]VPNAnnouncement, error) {
	ctx, cancel := context.WithTimeout(n.ctx, 20*time.Second)
	defer cancel()

	// 1. Primary: Find via DHT Providers
	providers, err := n.DHT.FindProviders(ctx, getVpnCid())
	var results []VPNAnnouncement
	seen := make(map[string]bool)
	seen[n.Host.ID().String()] = true // Never discover ourselves

	if err == nil {
		for _, p := range providers {
			if p.ID == n.Host.ID() { continue }
			
			key := "/zypo/vpn/" + p.ID.String()
			val, err := n.DHT.GetValue(ctx, key)
			if err == nil {
				var ann VPNAnnouncement
				if err := json.Unmarshal(val, &ann); err == nil {
					msg := []byte(fmt.Sprintf("%s|%s|%f|%d", ann.PeerID, ann.Location, ann.Price, ann.Timestamp))
					pub, _ := p.ID.ExtractPublicKey()
					if pub != nil {
						if valid, _ := pub.Verify(msg, ann.Signature); valid {
							results = append(results, ann)
							seen[ann.PeerID] = true
						}
					}
				}
			}
		}
	}

	// 2. Fallback: Check all directly connected peers.
	// This is much faster for local/LAN environments.
	for _, p := range n.Host.Network().Peers() {
		if seen[p.String()] { continue }
		
		// Try to fetch VPN status directly via P2P if they are not in DHT yet
		ctx2, cancel2 := context.WithTimeout(n.ctx, 2*time.Second)
		s, err := n.Host.NewStream(ctx2, p, ZypoProtocolID)
		cancel2()
		if err != nil { continue }
		
		req := ZypoRequest{Action: "vpn_probe"}
		reqBytes, _ := json.Marshal(req)
		s.Write(append(reqBytes, '\n'))
		
		br := bufio.NewReader(s)
		s.SetReadDeadline(time.Now().Add(2*time.Second))
		hLine, err := br.ReadString('\n')
		if err == nil {
			var header ZypoHeader
			if json.Unmarshal([]byte(hLine), &header) == nil && header.Status == 200 && header.Size > 0 {
				body := make([]byte, header.Size)
				_, err = io.ReadFull(br, body)
				if err == nil {
					var ann VPNAnnouncement
					if json.Unmarshal(body, &ann) == nil && ann.PeerID != "" {
						results = append(results, ann)
						seen[ann.PeerID] = true
					}
				}
			}
		}
		s.Close()
	}

	return results, nil
}

// AnnounceVPNPresence triggers periodic DHT and local announcements
func (n *ZypoNode) AnnounceVPNPresence() {
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		
		for {
			log.Printf("[VPN] Announcing VPN presence to mesh...")
			ctx, cancel := context.WithTimeout(n.ctx, 15*time.Second)
			if err := n.DHT.Provide(ctx, getVpnCid(), true); err != nil {
				log.Printf("[VPN] DHT Provide failed: %v", err)
			}
			cancel()

			ann := &VPNAnnouncement{
				PeerID:    n.Host.ID().String(),
				Location:  n.cfg.VpnLocation,
				Flag:      n.cfg.VpnFlag,
				Price:     n.cfg.VpnPrice,
				Bandwidth: n.cfg.VpnBandwidth,
				Timestamp: time.Now().Unix(),
			}
			
			// Command Center specific defaults (can still be overridden by config)
			if n.cfg.IsCommandCenter {
				if ann.Location == "Decentralized Node" || ann.Location == "" {
					ann.Location = "Zypo Command Center"
				}
				if ann.Flag == "🌐" || ann.Flag == "" {
					ann.Flag = "🏛️"
				}
				if ann.Price == 0.5 {
					ann.Price = 0.1
				}
				if ann.Bandwidth == 100 {
					ann.Bandwidth = 5000
				}
			}

			if err := n.RegisterVPNNodeDHT(ann); err != nil {
				log.Printf("[VPN] RegisterVPNNodeDHT failed: %v", err)
			}
			
			select {
			case <-ticker.C:
			case <-n.ctx.Done():
				return
			}
		}
	}()
}

