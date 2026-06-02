package client

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/dot-zypo/daemon/common/node"
)

var (
	certCache   = make(map[string]*tls.Certificate)
	certCacheMu sync.Mutex
)

func generateCert(host string) (*tls.Certificate, error) {
	certCacheMu.Lock()
	defer certCacheMu.Unlock()
	if c, ok := certCache[host]; ok {
		return c, nil
	}

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"Zypo Decentralized Network"},
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{host},
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return nil, err
	}

	cert := &tls.Certificate{
		Certificate: [][]byte{derBytes},
		PrivateKey:  priv,
	}

	certCache[host] = cert
	return cert, nil
}

type tlsResponseWriter struct {
	conn    net.Conn
	headers http.Header
	status  int
	wroteHeader bool
}

func (w *tlsResponseWriter) Header() http.Header {
	if w.headers == nil {
		w.headers = make(http.Header)
	}
	return w.headers
}

func (w *tlsResponseWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.conn.Write(b)
}

func (w *tlsResponseWriter) WriteHeader(statusCode int) {
	if w.wroteHeader {
		return
	}
	w.status = statusCode
	w.wroteHeader = true
	var buf bytes.Buffer
	buf.WriteString(fmt.Sprintf("HTTP/1.1 %d %s\r\n", statusCode, http.StatusText(statusCode)))
	for k, v := range w.headers {
		for _, val := range v {
			buf.WriteString(fmt.Sprintf("%s: %s\r\n", k, val))
		}
	}
	buf.WriteString("\r\n")
	w.conn.Write(buf.Bytes())
}

func handleCONNECT(n *node.ZypoNode, w http.ResponseWriter, r *http.Request) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "Hijacking not supported", http.StatusInternalServerError)
		return
	}
	conn, _, err := hijacker.Hijack()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	defer conn.Close()

	conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))

	host := r.Host
	if strings.Contains(host, ":") {
		host = strings.Split(host, ":")[0]
	}

	cert, err := generateCert(host)
	if err != nil {
		log.Printf("Failed to generate cert: %v", err)
		return
	}

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{*cert},
	}
	tlsConn := tls.Server(conn, tlsConfig)
	if err := tlsConn.Handshake(); err != nil {
		log.Printf("TLS handshake error for %s: %v", host, err)
		return
	}
	defer tlsConn.Close()

	reader := bufio.NewReader(tlsConn)
	req, err := http.ReadRequest(reader)
	if err != nil {
		log.Printf("Failed to read HTTP request inside TLS for %s: %v", host, err)
		return
	}
	req.URL.Scheme = "https"
	req.URL.Host = r.Host

	tw := &tlsResponseWriter{conn: tlsConn}
	handleForwardProxyRequest(n, tw, req)
}
