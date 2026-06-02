package node

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

type Transaction struct {
	ID        string  `json:"id"`
	From      string  `json:"from"`
	To        string  `json:"to"`
	Amount    float64 `json:"amount"`
	Time      int64   `json:"time"`
	Comment   string  `json:"comment"`
	Signature []byte  `json:"signature"`
}

type Account struct {
	Balance float64         `json:"balance"` // ZPCN
	History []Transaction   `json:"history"`
	Rating  float64         `json:"rating"`
	SeenTxs map[string]bool `json:"seen_txs"`
}

type EconomyManager struct {
	node           *ZypoNode
	dataDir        string
	accounts       map[string]*Account
	prepaidTraffic map[string]int64 // PeerID -> Bytes remaining
	mu             sync.RWMutex
}

func NewEconomyManager(node *ZypoNode, dataDir string) *EconomyManager {
	os.MkdirAll(dataDir, 0755)
	em := &EconomyManager{
		node:           node,
		dataDir:        dataDir,
		accounts:       make(map[string]*Account),
		prepaidTraffic: make(map[string]int64),
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
			if acc.SeenTxs == nil {
				acc.SeenTxs = make(map[string]bool)
			}
			if acc.Rating == 0 {
				acc.Rating = 5.0 // Default decentralized reputation
			}
			// Rebuild seen txs from history if needed
			for _, tx := range acc.History {
				acc.SeenTxs[tx.ID] = true
			}
		}
	}

	pt, err := os.ReadFile(filepath.Join(em.dataDir, "prepaid.json"))
	if err == nil {
		json.Unmarshal(pt, &em.prepaidTraffic)
	}

	// Initialize self if not exists
	myID := em.node.Host.ID().String()
	if _, ok := em.accounts[myID]; !ok {
		initialBalance := float64(0)
		if em.node.cfg.IsCommandCenter {
			initialBalance = 1000000 // CC acts as a genesis fund
		}
		em.accounts[myID] = &Account{
			Balance: initialBalance,
			History: make([]Transaction, 0),
			Rating:  5.0,
			SeenTxs: make(map[string]bool),
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
		tmpPath := filepath.Join(em.dataDir, "accounts.json.tmp")
		os.WriteFile(tmpPath, b, 0644)
		os.Rename(tmpPath, filepath.Join(em.dataDir, "accounts.json"))
	}
	b2, err := json.MarshalIndent(em.prepaidTraffic, "", "  ")
	if err == nil {
		tmpPath := filepath.Join(em.dataDir, "prepaid.json.tmp")
		os.WriteFile(tmpPath, b2, 0644)
		os.Rename(tmpPath, filepath.Join(em.dataDir, "prepaid.json"))
	}
}

// DumpState exports the entire account ledger (Oracle only)
func (em *EconomyManager) DumpState() []byte {
	em.mu.RLock()
	defer em.mu.RUnlock()
	b, _ := json.Marshal(em.accounts)
	return b
}

// MergeState imports the ledger from Oracle
func (em *EconomyManager) MergeState(data []byte) error {
	em.mu.Lock()
	defer em.mu.Unlock()
	
	var oracleAccounts map[string]*Account
	if err := json.Unmarshal(data, &oracleAccounts); err != nil {
		return err
	}
	
	for pid, oAcc := range oracleAccounts {
		if _, ok := em.accounts[pid]; !ok {
			em.accounts[pid] = &Account{SeenTxs: make(map[string]bool)}
		}
		acc := em.accounts[pid]
		acc.Balance = oAcc.Balance
		acc.Rating = oAcc.Rating
		
		// Merge history and seen txs safely
		for _, tx := range oAcc.History {
			if !acc.SeenTxs[tx.ID] {
				acc.SeenTxs[tx.ID] = true
				acc.History = append(acc.History, tx)
			}
		}
	}
	
	em.saveLocked()
	log.Printf("[Economy] Synced ledger from Oracle (%d accounts updated)", len(oracleAccounts))
	return nil
}

// SyncWithOracle connects to Oracle and updates ledger
func (em *EconomyManager) SyncWithOracle() {
	if em.node.cfg.IsCommandCenter {
		return // Oracle doesn't sync with itself
	}
	if em.node.validator == nil || em.node.validator.OraclePeerID == "" {
		return
	}
	
	oracleID, err := peer.Decode(em.node.validator.OraclePeerID)
	if err != nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	s, err := em.node.Host.NewStream(ctx, oracleID, ZypoProtocolID)
	if err != nil {
		log.Printf("[Economy] Failed to connect to Oracle for sync: %v", err)
		return
	}
	defer s.Close()

	req := map[string]interface{}{"action": "economy_dump"}
	reqBytes, _ := json.Marshal(req)
	s.Write(append(reqBytes, '\n'))

	reader := bufio.NewReader(s)
	hLine, err := reader.ReadString('\n')
	if err != nil {
		return
	}

	var header ZypoHeader
	if err := json.Unmarshal([]byte(hLine), &header); err != nil {
		return
	}

	if header.Status == 200 {
		data := make([]byte, header.Size)
		io.ReadFull(reader, data)
		em.MergeState(data)
	}
}

func (em *EconomyManager) GetBalance(peerID string) float64 {
	em.mu.RLock()
	defer em.mu.RUnlock()
	if acc, ok := em.accounts[peerID]; ok {
		return acc.Balance
	}
	return 0 // No implicit welcome bonus
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

	pub, err := fromID.ExtractPublicKey()
	if err != nil {
		pub = em.node.Host.Peerstore().PubKey(fromID)
		if pub == nil {
			return fmt.Errorf("public key for %s not found", tx.From)
		}
	}

	msg := []byte(fmt.Sprintf("%s|%s|%f|%d", tx.From, tx.To, tx.Amount, tx.Time))
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

	if _, ok := em.accounts[tx.From]; !ok {
		em.accounts[tx.From] = &Account{Balance: 0, Rating: 5.0, SeenTxs: make(map[string]bool)}
	}
	if _, ok := em.accounts[tx.To]; !ok {
		em.accounts[tx.To] = &Account{Balance: 0, Rating: 5.0, SeenTxs: make(map[string]bool)}
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
	if em.node.cfg.IsCommandCenter && tx.From == em.node.Host.ID().String() {
		isOracle = true
	}

	creditLimit := fromAcc.Rating * 2.0 // Credit line based on decentralized reputation
	if fromAcc.Balance+creditLimit < tx.Amount && !isOracle {
		// Sync with Oracle and try once more before failing
		if em.node.validator != nil && em.node.validator.OraclePeerID != "" && !em.node.cfg.IsCommandCenter {
			em.mu.Unlock()
			em.SyncWithOracle()
			em.mu.Lock()
			fromAcc = em.accounts[tx.From] // Re-fetch after sync
			if fromAcc == nil || fromAcc.Balance+(fromAcc.Rating*2.0) < tx.Amount {
				return fmt.Errorf("insufficient funds and exhausted credit limit")
			}
		} else {
			return fmt.Errorf("insufficient funds and exhausted credit limit")
		}
	}

	// Replay protection O(1)
	if fromAcc.SeenTxs[tx.ID] {
		return fmt.Errorf("tx already processed")
	}

	fromAcc.Balance -= tx.Amount
	toAcc.Balance += tx.Amount

	fromAcc.SeenTxs[tx.ID] = true
	fromAcc.History = append(fromAcc.History, *tx)
	
	if tx.From != tx.To {
		toAcc.SeenTxs[tx.ID] = true
		toAcc.History = append(toAcc.History, *tx)
	}

	// Cap history size (unless we are the CC, which needs full audit logs)
	if !em.node.cfg.IsCommandCenter {
		if len(fromAcc.History) > 100 {
			fromAcc.History = fromAcc.History[len(fromAcc.History)-100:]
		}
		if len(toAcc.History) > 100 {
			toAcc.History = toAcc.History[len(toAcc.History)-100:]
		}
	}

	// Process prepaid VPN logic
	if tx.To == em.node.Host.ID().String() && tx.Comment == "VPN Prepay" {
		price := em.node.cfg.VpnPrice
		if price <= 0 {
			price = 0.5
		}
		// Calculate bytes based on Amount (assumed 1 Amount = 1 ZPCN)
		// 1 ZPCN = (1GB / price). Example: if price is 0.5 ZPCN/GB, 1 ZPCN = 2GB.
		gb := float64(tx.Amount) / price
		bytesToAdd := int64(gb * 1024 * 1024 * 1024)
		em.prepaidTraffic[tx.From] += bytesToAdd
	}

	em.saveLocked()
	log.Printf("💰 Economy: Processed TX %s (%s -> %s: %d ZPCN)", tx.ID, tx.From[:8], tx.To[:8], tx.Amount)
	return nil
}

func (em *EconomyManager) CreateAndSendTransaction(to string, amount float64, comment string) (*Transaction, error) {
	myID := em.node.Host.ID().String()

	em.mu.RLock()
	acc := em.accounts[myID]
	if acc == nil || acc.Balance < amount {
		em.mu.RUnlock()
		return nil, fmt.Errorf("insufficient funds")
	}
	em.mu.RUnlock()

	ts := time.Now().UnixNano() / int64(time.Millisecond)
	msg := []byte(fmt.Sprintf("%s|%s|%f|%d", myID, to, amount, ts))
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

	if err := em.sendTxToPeer(tx); err != nil {
		em.rollbackTransaction(tx)
		return nil, fmt.Errorf("failed to deliver transaction, rolled back locally: %v", err)
	}
	return tx, nil
}

func (em *EconomyManager) rollbackTransaction(tx *Transaction) {
	em.mu.Lock()
	defer em.mu.Unlock()

	fromAcc := em.accounts[tx.From]
	toAcc := em.accounts[tx.To]

	if fromAcc != nil {
		fromAcc.Balance += tx.Amount
		delete(fromAcc.SeenTxs, tx.ID)
		for i, h := range fromAcc.History {
			if h.ID == tx.ID {
				fromAcc.History = append(fromAcc.History[:i], fromAcc.History[i+1:]...)
				break
			}
		}
	}

	if toAcc != nil && tx.From != tx.To {
		toAcc.Balance -= tx.Amount
		delete(toAcc.SeenTxs, tx.ID)
		for i, h := range toAcc.History {
			if h.ID == tx.ID {
				toAcc.History = append(toAcc.History[:i], toAcc.History[i+1:]...)
				break
			}
		}
	}

	em.saveLocked()
	log.Printf("💰 Economy: Rolled back TX %s", tx.ID)
}

// ProcessFaucet dispenses 10 ZPCN to the given peer once per 24 hours
func (em *EconomyManager) ProcessFaucet(peerID string) error {
	em.mu.Lock()
	acc, ok := em.accounts[peerID]
	if !ok {
		acc = &Account{Balance: 0, SeenTxs: make(map[string]bool)}
		em.accounts[peerID] = acc
	}
	em.mu.Unlock()

	// Faucet limits are handled externally (e.g., in hosting.go) to keep economy tracking clean.
	_, err := em.CreateAndSendTransaction(peerID, 10.0, "Faucet ZPCN Dispense")
	return err
}

func (em *EconomyManager) sendTxToPeer(tx *Transaction) error {
	toID, err := peer.Decode(tx.To)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(em.node.ctx, 10*time.Second)
	defer cancel()

	s, err := em.node.Host.NewStream(ctx, toID, ZypoProtocolID)
	if err != nil {
		return err
	}

	req := map[string]interface{}{
		"action": "economy_tx",
	}
	reqBytes, _ := json.Marshal(req)
	s.Write(append(reqBytes, '\n'))

	txBytes, _ := json.Marshal(tx)
	s.Write(append(txBytes, '\n'))
	
	// Close write end to signal EOF to the server
	s.CloseWrite()

	// Wait for response to ensure the provider processed the prepayment
	reader := bufio.NewReader(s)
	s.SetReadDeadline(time.Now().Add(5 * time.Second))
	respLine, err := reader.ReadString('\n')
	s.Close()
	
	if err != nil {
		return fmt.Errorf("failed to read economy_tx response: %v", err)
	}
	var resp ZypoHeader
	if err := json.Unmarshal([]byte(respLine), &resp); err != nil {
		return fmt.Errorf("invalid economy_tx response")
	}
	if resp.Status != 200 {
		return fmt.Errorf("provider rejected transaction")
	}
	return nil
}

// VPN Accounting Support
func (em *EconomyManager) AddPrepaidTraffic(peerID string, bytes int64) {
	em.mu.Lock()
	defer em.mu.Unlock()
	em.prepaidTraffic[peerID] += bytes
	em.saveLocked()
}

func (em *EconomyManager) GetPrepaidTraffic(peerID string) int64 {
	em.mu.RLock()
	defer em.mu.RUnlock()
	return em.prepaidTraffic[peerID]
}

func (em *EconomyManager) ConsumePrepaidTraffic(peerID string, bytes int64) bool {
	em.mu.Lock()
	defer em.mu.Unlock()
	if em.prepaidTraffic[peerID] >= bytes {
		em.prepaidTraffic[peerID] -= bytes
		// We don't save to disk on every byte to avoid IO bottleneck.
		// Save periodically or on disconnect.
		return true
	}
	return false
}

func (em *EconomyManager) SettleVPNTicket(ticketBytes []byte) error {
	type PaymentTicket struct {
		ChannelID string  `json:"channel_id"`
		Amount    float64 `json:"amount_total"`
		Nonce     int     `json:"nonce"`
		Consumer  string  `json:"consumer"`
		Provider  string  `json:"provider"`
		Signature []byte  `json:"signature"`
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

	msg := []byte(fmt.Sprintf("%s|%f|%d", ticket.ChannelID, ticket.Amount, ticket.Nonce))
	valid, err := pub.Verify(msg, ticket.Signature)
	if err != nil || !valid {
		return fmt.Errorf("invalid ticket signature")
	}

	tx := &Transaction{
		ID:        fmt.Sprintf("vpn-%s-%d", ticket.ChannelID, ticket.Nonce),
		From:      ticket.Consumer,
		To:        ticket.Provider,
		Amount:    ticket.Amount,
		Time:      time.Now().UnixNano() / int64(time.Millisecond),
		Comment:   "VPN Service Settlement",
		Signature: ticket.Signature, 
	}

	return em.ProcessTransaction(tx)
}
