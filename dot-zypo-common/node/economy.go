package node

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

type Transaction struct {
	ID        string `json:"id"`
	From      string `json:"from"`
	To        string `json:"to"`
	Amount    int64  `json:"amount"`
	Time      int64  `json:"time"`
	Comment   string `json:"comment"`
	Signature []byte `json:"signature"`
}

type Account struct {
	Balance int64         `json:"balance"` // ZPCN
	History []Transaction `json:"history"`
	Rating  float64       `json:"rating"`
}

type EconomyManager struct {
	node     *ZypoNode
	dataDir  string
	accounts map[string]*Account
	mu       sync.RWMutex
}

func NewEconomyManager(node *ZypoNode, dataDir string) *EconomyManager {
	os.MkdirAll(dataDir, 0755)
	em := &EconomyManager{
		node:     node,
		dataDir:  dataDir,
		accounts: make(map[string]*Account),
	}
	em.load()
	return em
}

func (em *EconomyManager) load() {
	em.mu.Lock()
	defer em.mu.Unlock()
	b, err := os.ReadFile(filepath.Join(em.dataDir, "accounts.json"))
	if err == nil {
		json.Unmarshal(b, &em.accounts)
		for _, acc := range em.accounts {
			if acc.Rating == 0 {
				acc.Rating = 5.0
			}
		}
	}
	// Initialize self if not exists
	myID := em.node.Host.ID().String()
	if _, ok := em.accounts[myID]; !ok {
		initialBalance := int64(100)
		if em.node.cfg.IsCommandCenter {
			initialBalance = 1000000 // CC acts as a genesis fund
		}
		em.accounts[myID] = &Account{
			Balance: initialBalance,
			History: make([]Transaction, 0),
			Rating:  5.0,
		}
		em.saveLocked()
	}

	// Force CC to always have enough funds
	if em.node.cfg.IsCommandCenter && em.accounts[myID].Balance < 1000000 {
		em.accounts[myID].Balance = 1000000000 // Infinite money for CC
		em.saveLocked()
	}
}

func (em *EconomyManager) saveLocked() {
	b, err := json.MarshalIndent(em.accounts, "", "  ")
	if err == nil {
		os.WriteFile(filepath.Join(em.dataDir, "accounts.json"), b, 0644)
	}
}

func (em *EconomyManager) GetBalance(peerID string) int64 {
	em.mu.RLock()
	defer em.mu.RUnlock()
	if acc, ok := em.accounts[peerID]; ok {
		return acc.Balance
	}
	// Implicit welcome bonus for unknown peers in this simplified P2P ledger
	return 100
}

func (em *EconomyManager) GetAccount(peerID string) *Account {
	em.mu.RLock()
	defer em.mu.RUnlock()
	if acc, ok := em.accounts[peerID]; ok {
		return acc
	}
	return nil
}

func (em *EconomyManager) verifyTx(tx *Transaction) error {
	fromID, err := peer.Decode(tx.From)
	if err != nil {
		return err
	}

	// We need the public key to verify. In libp2p, ID contains the public key if it's small (like Ed25519)
	pub, err := fromID.ExtractPublicKey()
	if err != nil {
		// Try to get from peerstore
		pub = em.node.Host.Peerstore().PubKey(fromID)
		if pub == nil {
			return fmt.Errorf("public key for %s not found", tx.From)
		}
	}

	msg := []byte(fmt.Sprintf("%s|%s|%d|%d", tx.From, tx.To, tx.Amount, tx.Time))
	valid, err := pub.Verify(msg, tx.Signature)
	if err != nil || !valid {
		return fmt.Errorf("invalid signature")
	}
	return nil
}

func (em *EconomyManager) ProcessTransaction(tx *Transaction) error {
	if err := em.verifyTx(tx); err != nil {
		return err
	}

	em.mu.Lock()
	defer em.mu.Unlock()

	// Ensure accounts exist
	if _, ok := em.accounts[tx.From]; !ok {
		em.accounts[tx.From] = &Account{Balance: 100}
	}
	if _, ok := em.accounts[tx.To]; !ok {
		em.accounts[tx.To] = &Account{Balance: 100}
	}

	fromAcc := em.accounts[tx.From]
	toAcc := em.accounts[tx.To]

	// Determine if sender is the Command Center (Oracle)
	isOracle := false
	fromPeerID, _ := peer.Decode(tx.From)
	if em.node.validator != nil && em.node.validator.OraclePubKey != nil {
		oracleID, err := peer.IDFromPublicKey(em.node.validator.OraclePubKey)
		if err == nil && fromPeerID == oracleID {
			isOracle = true
		}
	}
	// Also if we are the CC ourselves
	if em.node.cfg.IsCommandCenter && tx.From == em.node.Host.ID().String() {
		isOracle = true
	}

	if fromAcc.Balance < tx.Amount && !isOracle {
		return fmt.Errorf("insufficient funds")
	}

	// Check for replay (simplified: just check ID)
	for _, h := range fromAcc.History {
		if h.ID == tx.ID {
			return fmt.Errorf("tx already processed")
		}
	}

	fromAcc.Balance -= tx.Amount
	toAcc.Balance += tx.Amount

	fromAcc.History = append(fromAcc.History, *tx)
	if tx.From != tx.To {
		toAcc.History = append(toAcc.History, *tx)
	}

	em.saveLocked()
	log.Printf("💰 Economy: Processed TX %s (%s -> %s: %d ZPCN)", tx.ID, tx.From[:8], tx.To[:8], tx.Amount)
	return nil
}

func (em *EconomyManager) CreateAndSendTransaction(to string, amount int64, comment string) (*Transaction, error) {
	myID := em.node.Host.ID().String()

	em.mu.RLock()
	acc := em.accounts[myID]
	if acc == nil || acc.Balance < amount {
		em.mu.RUnlock()
		return nil, fmt.Errorf("insufficient funds")
	}
	em.mu.RUnlock()

	ts := time.Now().UnixNano() / int64(time.Millisecond)
	msg := []byte(fmt.Sprintf("%s|%s|%d|%d", myID, to, amount, ts))
	sig, err := em.node.PrivKey.Sign(msg)
	if err != nil {
		return nil, err
	}

	tx := &Transaction{
		ID:        fmt.Sprintf("tx-%d", ts),
		From:      myID,
		To:        to,
		Amount:    amount,
		Time:      ts,
		Comment:   comment,
		Signature: sig,
	}

	if err := em.ProcessTransaction(tx); err != nil {
		return nil, err
	}

	// Try to notify the recipient directly
	go em.sendTxToPeer(tx)

	return tx, nil
}

func (em *EconomyManager) sendTxToPeer(tx *Transaction) {
	toID, err := peer.Decode(tx.To)
	if err != nil {
		return
	}

	ctx, cancel := context.WithTimeout(em.node.ctx, 10*time.Second)
	defer cancel()

	s, err := em.node.Host.NewStream(ctx, toID, ZypoProtocolID)
	if err != nil {
		return
	}
	defer s.Close()

	req := map[string]interface{}{
		"action": "economy_tx",
	}
	reqBytes, _ := json.Marshal(req)
	s.Write(append(reqBytes, '\n'))

	txBytes, _ := json.Marshal(tx)
	s.Write(append(txBytes, '\n'))
}

// Support for VPN Settlement
func (em *EconomyManager) SettleVPNTicket(ticketBytes []byte) error {
	// A VPN ticket is just a structured message signed by the consumer
	type PaymentTicket struct {
		ChannelID string `json:"channel_id"`
		Amount    int64  `json:"amount_total"`
		Nonce     int    `json:"nonce"`
		Consumer  string `json:"consumer"`
		Provider  string `json:"provider"`
		Signature []byte `json:"signature"`
	}
	var ticket PaymentTicket
	if err := json.Unmarshal(ticketBytes, &ticket); err != nil {
		return err
	}

	consumerID, err := peer.Decode(ticket.Consumer)
	if err != nil {
		return err
	}

	pub, err := consumerID.ExtractPublicKey()
	if err != nil {
		pub = em.node.Host.Peerstore().PubKey(consumerID)
		if pub == nil {
			return fmt.Errorf("public key not found")
		}
	}

	msg := []byte(fmt.Sprintf("%s|%d|%d", ticket.ChannelID, ticket.Amount, ticket.Nonce))
	valid, err := pub.Verify(msg, ticket.Signature)
	if err != nil || !valid {
		return fmt.Errorf("invalid ticket signature")
	}

	// Convert ticket into a transaction locally
	tx := &Transaction{
		ID:        fmt.Sprintf("vpn-%s-%d", ticket.ChannelID, ticket.Nonce),
		From:      ticket.Consumer,
		To:        ticket.Provider,
		Amount:    ticket.Amount,
		Time:      time.Now().UnixNano() / int64(time.Millisecond),
		Comment:   "VPN Service Settlement",
		Signature: ticket.Signature, // Store the original ticket signature as proof
	}

	return em.ProcessTransaction(tx)
}
