package node

import (
	"fmt"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

type ZypoRecord struct {
	Domain    string   `json:"domain"`
	OwnerID   string   `json:"owner_id"`
	HostIDs   []string `json:"host_ids"`
	Signature []byte   `json:"signature"`
}

func SignRecord(priv crypto.PrivKey, domain, ownerID string, hostIDs []string) (*ZypoRecord, error) {
	// Join HostIDs consistently for signing
	hostsStr := ""
	if len(hostIDs) > 0 {
		hostsStr = hostIDs[0]
		for i := 1; i < len(hostIDs); i++ {
			hostsStr += "," + hostIDs[i]
		}
	}
	msg := []byte(fmt.Sprintf("%s|%s|%s", domain, ownerID, hostsStr))
	sig, err := priv.Sign(msg)
	if err != nil {
		return nil, err
	}
	return &ZypoRecord{
		Domain:    domain,
		OwnerID:   ownerID,
		HostIDs:   hostIDs,
		Signature: sig,
	}, nil
}

func VerifyRecord(pub crypto.PubKey, record *ZypoRecord) (bool, error) {
	hostsStr := ""
	if len(record.HostIDs) > 0 {
		hostsStr = record.HostIDs[0]
		for i := 1; i < len(record.HostIDs); i++ {
			hostsStr += "," + record.HostIDs[i]
		}
	}
	msg := []byte(fmt.Sprintf("%s|%s|%s", record.Domain, record.OwnerID, hostsStr))
	return pub.Verify(msg, record.Signature)
}

func ExtractOraclePeerID(bootstrapAddrs []string) (string, error) {
	for _, addr := range bootstrapAddrs {
		m, err := multiaddr.NewMultiaddr(addr)
		if err != nil {
			continue
		}
		info, err := peer.AddrInfoFromP2pAddr(m)
		if err == nil {
			return info.ID.String(), nil
		}
	}
	return "", fmt.Errorf("no valid bootstrap peer id found")
}

func GetOraclePublicKey(oraclePeerIDStr string) (crypto.PubKey, error) {
	id, err := peer.Decode(oraclePeerIDStr)
	if err != nil {
		return nil, err
	}
	pub, err := id.ExtractPublicKey()
	if err != nil {
		return nil, err
	}
	return pub, nil
}
