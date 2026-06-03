package main
import "gvisor.dev/gvisor/pkg/tcpip"
func main() { _ = tcpip.TCPReceiveBufferSizeRangeOption{} }
