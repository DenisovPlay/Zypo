import sys

node_path = 'dot-zypo-common/node/node.go'
with open(node_path, 'r') as f:
    node_content = f.read()

# Add Close method
if 'func (n *ZypoNode) Close()' not in node_content:
    node_content += '''
func (n *ZypoNode) Close() error {
\tlog.Println("[Node] Initiating Graceful Shutdown...")
\tvar err error
\tif n.DHT != nil {
\t\terr = n.DHT.Close()
\t}
\tif n.Host != nil {
\t\tif e := n.Host.Close(); e != nil {
\t\t\terr = e
\t\t}
\t}
\tlog.Println("[Node] Shutdown complete")
\treturn err
}
'''

# Update Announce IP logic
old_relay = 'libp2p.EnableRelayService(), // CAN BE A RELAY (Bridge) if it has a public IP\\n\\t\\tlibp2p.Security(noise.ID, noise.New),'
new_relay = '''libp2p.EnableRelayService(), // CAN BE A RELAY (Bridge) if it has a public IP
\t\tlibp2p.Security(noise.ID, noise.New),
\t\tlibp2p.AddrsFactory(func(addrs []multiaddr.Multiaddr) []multiaddr.Multiaddr {
\t\t\tannounceIP := os.Getenv("ZYPO_ANNOUNCE_IP")
\t\t\tif announceIP == "" {
\t\t\t\treturn addrs
\t\t\t}
\t\t\tvar announced []multiaddr.Multiaddr
\t\t\tfor _, a := range addrs {
\t\t\t\tif p, err := a.ValueForProtocol(multiaddr.P_TCP); err == nil {
\t\t\t\t\tma, _ := multiaddr.NewMultiaddr(fmt.Sprintf("/ip4/%s/tcp/%s", announceIP, p))
\t\t\t\t\tannounced = append(announced, ma)
\t\t\t\t}
\t\t\t\tif p, err := a.ValueForProtocol(multiaddr.P_UDP); err == nil {
\t\t\t\t\tma, _ := multiaddr.NewMultiaddr(fmt.Sprintf("/ip4/%s/udp/%s/quic-v1", announceIP, p))
\t\t\t\t\tannounced = append(announced, ma)
\t\t\t\t}
\t\t\t}
\t\t\treturn announced
\t\t}),'''
if 'ZYPO_ANNOUNCE_IP' not in node_content:
    node_content = node_content.replace(old_relay.replace('\\n', '\n'), new_relay)

with open(node_path, 'w') as f:
    f.write(node_content)

protocol_path = 'dot-zypo-common/node/protocol.go'
with open(protocol_path, 'r') as f:
    protocol_content = f.read()

# Update 10MB limit
old_limit = '''\t} else if req.Method == "GET" || req.Method == "HEAD" {
\t\tbodyReader = bytes.NewReader(nil)
\t}'''
new_limit = '''\t} else if req.Method == "GET" || req.Method == "HEAD" {
\t\tbodyReader = bytes.NewReader(nil)
\t} else {
\t\tbodyReader = io.LimitReader(reader, 10*1024*1024)
\t}'''
if '10*1024*1024' not in protocol_content:
    protocol_content = protocol_content.replace(old_limit, new_limit)

# Update json parse err
old_json = '''\t\t\ttxData, _ := io.ReadAll(bodyReader)
\t\t\tvar tx Transaction
\t\t\tif err := json.Unmarshal(txData, &tx); err == nil {'''
new_json = '''\t\t\ttxData, err := io.ReadAll(bodyReader)
\t\t\tif err != nil {
\t\t\t\theader = ZypoHeader{Status: 400}
\t\t\t} else {
\t\t\t\tvar tx Transaction
\t\t\t\tif err := json.Unmarshal(txData, &tx); err == nil {'''
if 'txData, err :=' not in protocol_content:
    protocol_content = protocol_content.replace(old_json, new_json)
    # also add closing brace
    old_close = '''\t\t\t\t\theader = ZypoHeader{Status: 400}
\t\t\t\t}
\t\t\t} else {'''
    new_close = '''\t\t\t\t\theader = ZypoHeader{Status: 400}
\t\t\t\t}
\t\t\t}
\t\t\t} else {'''
    protocol_content = protocol_content.replace(old_close, new_close)

with open(protocol_path, 'w') as f:
    f.write(protocol_content)

