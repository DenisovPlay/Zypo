package node

import (
	"crypto/rand"
	"fmt"

	"github.com/flynn/noise"
)

// SecureChannel implements Noise Protocol Handshake XX.
type SecureChannel struct {
	hs            *noise.HandshakeState
	sendCipher    *noise.CipherState
	recvCipher    *noise.CipherState
	HandshakeDone bool
	initiator     bool
	step          int
}

func NewSecureChannel(initiator bool) (*SecureChannel, error) {
	// Noise XX requires static keys to be exchanged.
	// We generate a temporary static keypair for this session.
	staticKey, err := noise.DH25519.GenerateKeypair(rand.Reader)
	if err != nil {
		return nil, err
	}

	conf := noise.Config{
		CipherSuite:   noise.NewCipherSuite(noise.DH25519, noise.CipherAESGCM, noise.HashSHA256),
		Random:        rand.Reader,
		Pattern:       noise.HandshakeXX,
		Initiator:     initiator,
		StaticKeypair: staticKey,
	}

	hs, err := noise.NewHandshakeState(conf)
	if err != nil {
		return nil, err
	}

	return &SecureChannel{hs: hs, initiator: initiator, step: 0}, nil
}

func (sc *SecureChannel) StepCount() int {
	return sc.step
}

func (sc *SecureChannel) Step(in []byte) ([]byte, error) {
	if sc.HandshakeDone {
		return nil, fmt.Errorf("handshake already complete")
	}

	var out []byte
	var cs1, cs2 *noise.CipherState
	var err error

	isWriting := false
	if sc.initiator {
		isWriting = (sc.step == 0 || sc.step == 2)
	} else {
		isWriting = (sc.step == 1)
	}

	if isWriting {
		out, cs1, cs2, err = sc.hs.WriteMessage(nil, in)
	} else {
		_, cs1, cs2, err = sc.hs.ReadMessage(nil, in)
		out = nil
	}

	if err != nil {
		return nil, err
	}

	sc.step++

	if cs1 != nil && cs2 != nil {
		if sc.initiator {
			sc.sendCipher = cs1
			sc.recvCipher = cs2
		} else {
			sc.sendCipher = cs2
			sc.recvCipher = cs1
		}
		sc.HandshakeDone = true
	}

	return out, nil
}

func (sc *SecureChannel) Encrypt(payload []byte) ([]byte, error) {
	if sc.sendCipher == nil {
		return nil, fmt.Errorf("not ready")
	}
	return sc.sendCipher.Encrypt(nil, nil, payload)
}

func (sc *SecureChannel) Decrypt(data []byte) ([]byte, error) {
	if sc.recvCipher == nil {
		return nil, fmt.Errorf("not ready")
	}
	return sc.recvCipher.Decrypt(nil, nil, data)
}
