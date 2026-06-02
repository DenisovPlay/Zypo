package node

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/libp2p/go-libp2p/core/crypto"
)

type ZypoValidator struct {
	OraclePubKey crypto.PubKey
}

func (v *ZypoValidator) Validate(key string, value []byte) error {
	if strings.HasPrefix(key, "/zypo/vpn/") {
		// Basic validation for VPN announcements
		var ann VPNAnnouncement
		if err := json.Unmarshal(value, &ann); err != nil {
			return fmt.Errorf("failed to unmarshal vpn announcement: %w", err)
		}
		// In a production app, we'd verify the signature here too
		return nil
	}

	if !strings.HasPrefix(key, "/zypo/dns/") {
		return fmt.Errorf("invalid key namespace: %s", key)
	}

	domain := strings.TrimPrefix(key, "/zypo/dns/")
	if domain == "" {
		return fmt.Errorf("empty domain")
	}

	var record ZypoRecord
	if err := json.Unmarshal(value, &record); err != nil {
		return fmt.Errorf("failed to unmarshal zypo record: %w", err)
	}

	if record.Domain != domain {
		return fmt.Errorf("domain mismatch: key=%s, record=%s", domain, record.Domain)
	}

	if v.OraclePubKey == nil {
		// MESH-ONLY MODE FALLBACK: If CC is unreachable and we have no Oracle key, 
		// we trust the DHT record blindly. This is insecure against spoofing, but keeps the network alive.
		return nil
	}

	valid, err := VerifyRecord(v.OraclePubKey, &record)
	if err != nil {
		return fmt.Errorf("signature verification failed: %w", err)
	}
	if !valid {
		return fmt.Errorf("invalid oracle signature for domain %s", domain)
	}

	return nil
}

func (v *ZypoValidator) Select(key string, values [][]byte) (int, error) {
	for i, val := range values {
		if err := v.Validate(key, val); err == nil {
			return i, nil
		}
	}
	return -1, fmt.Errorf("no valid values")
}
