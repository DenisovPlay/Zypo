package node

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	IsCommandCenter   bool     `json:"is_command_center"`
	IsServerNode      bool     `json:"is_server_node"`
	IsGateway         bool     `json:"is_gateway"`
	GatewayPort       int      `json:"gateway_port"`
	ListenPort        int      `json:"listen_port"`
	RpcPort           int      `json:"rpc_port"`
	RpcToken          string   `json:"rpc_token"`
	DashPort          int      `json:"dash_port"`
	DashUser          string   `json:"dash_user"`
	DashPass          string   `json:"dash_pass"`
	ForwardProxyPort  int      `json:"forward_proxy_port"`
	KeyFile           string   `json:"key_file"`
	DataDir           string   `json:"data_dir"`
	SitesDir          string   `json:"sites_dir"`
	EnableMdns        bool     `json:"enable_mdns"`
	EnableVpn         bool     `json:"enable_vpn"`
	BootstrapNodes    []string `json:"bootstrap_nodes"`
	DiscoveryURLs     []string `json:"discovery_urls"`
	CommandCenterAddr string   `json:"command_center_addr"` // Multiaddr of the command center for relay

	// VPN Service Configuration
	VpnLocation  string  `json:"vpn_location"`
	VpnFlag      string  `json:"vpn_flag"`
	VpnPrice     float64 `json:"vpn_price"`
	VpnBandwidth int     `json:"vpn_bandwidth"`

	// Official domains controlled by this network
	OfficialDomains []string `json:"official_domains"`
}

func LoadConfig(path string) (Config, error) {
	var cfg Config
	b, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	err = json.Unmarshal(b, &cfg)
	if err != nil {
		return cfg, fmt.Errorf("config parse error (DO NOT OVERWRITE): %w", err)
	}

	// Environment overrides
	if envPort := os.Getenv("ZYPO_LISTEN_PORT"); envPort != "" {
		fmt.Sscanf(envPort, "%d", &cfg.ListenPort)
	}
	if envCC := os.Getenv("ZYPO_COMMAND_CENTER"); envCC != "" {
		cfg.CommandCenterAddr = envCC
	}
	if envBoot := os.Getenv("ZYPO_BOOTSTRAP_NODES"); envBoot != "" {
		cfg.BootstrapNodes = strings.Split(envBoot, ",")
	}

	// Enforce absolute paths for Docker stability if not absolute
	if !filepath.IsAbs(cfg.DataDir) && cfg.DataDir != "" {
		if pwd, err := os.Getwd(); err == nil {
			cfg.DataDir = filepath.Join(pwd, cfg.DataDir)
		}
	}
	if !filepath.IsAbs(cfg.SitesDir) && cfg.SitesDir != "" {
		if pwd, err := os.Getwd(); err == nil {
			cfg.SitesDir = filepath.Join(pwd, cfg.SitesDir)
		}
	}

	return cfg, nil
}

func SaveConfig(path string, cfg Config) error {
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0644)
}

func DefaultConfig() Config {
	return Config{
		IsCommandCenter:  false,
		ListenPort:       0, // Random port for client nodes
		RpcPort:          8902,
		RpcToken:         "zypo_default_secret",
		DashPort:         0,
		DashUser:         "admin",
		DashPass:         "zypo",
		ForwardProxyPort: 8903,
		GatewayPort:      8080,
		KeyFile:          "client_key.bin",
		DataDir:          "data",
		SitesDir:         "zypo_sites",
		EnableMdns:       true,
		BootstrapNodes: []string{
			"/ip4/213.171.27.234/tcp/8900/p2p/12D3KooWPyuxuDGwsnBB6sKvCarjfRimWhNvvXaAhYnGkesBFbEf", // Zypo Cloud Relay
			"/dnsaddr/bootstrap.libp2p.io/p2p/QmNnooDuMpbfRAsEqJZsM3TzRVz5T1NDN4F2J5c581q4Yt",       // IPFS Public
			"/dnsaddr/bootstrap.libp2p.io/p2p/QmQCU2EcMqAqQPR2i9bChDtGNJchTbq5TbXBPxY8macTZX",       // IPFS Public
		},
		DiscoveryURLs:     []string{},
		CommandCenterAddr: "/ip4/213.171.27.234/tcp/8900/p2p/12D3KooWPyuxuDGwsnBB6sKvCarjfRimWhNvvXaAhYnGkesBFbEf",
		VpnLocation:       "Decentralized Node",
		VpnFlag:           "🌐",
		VpnPrice:          0.5,
		VpnBandwidth:      100,
		OfficialDomains:   []string{"gov.zypo", "domains.zypo", "docs.zypo", "api.zypo"},
	}
}
