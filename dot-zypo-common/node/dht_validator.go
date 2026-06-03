package node

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/libp2p/go-libp2p/core/crypto"
)

type ZypoValidator struct {
	OraclePubKey crypto.PubKey
	OraclePeerID string
}

func (v *ZypoValidator) Validate(key string, value []byte) error {
	if strings.HasPrefix(key, "/zypo/vpn/") {
		// Basic validation for VPN announcements
		var ann VPNAnnouncement
		if err := json.Unmarshal(value, &ann); err != nil {
			return fmt.Errorf("failed to unmarshal vpn announcement: %w", err)
		}
		// Signature is verified in DiscoverVPNNodes; here we just check structure.
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

	// SECURITY: Always require a non-empty signature in the record.
	// An unsigned record is always rejected regardless of Oracle key status.
	if len(record.Signature) == 0 {
		return fmt.Errorf("rejected unsigned DNS record for %s", domain)
	}

	if v.OraclePubKey == nil {
		// SECURITY: Oracle key is not known yet.
		// We refuse to accept any DNS record we cannot verify — this prevents
		// attackers from poisoning the DHT before we learn the real Oracle key.
		// The node must connect to the Command Center first to obtain the key.
		return fmt.Errorf("rejected DNS record for %s: Oracle public key not yet known (connect to CC first)", domain)
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
