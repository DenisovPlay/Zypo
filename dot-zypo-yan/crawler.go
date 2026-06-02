package main

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/dot-zypo/daemon/common/node"
	"github.com/libp2p/go-libp2p/core/peer"
	_ "modernc.org/sqlite"
	"golang.org/x/net/html"
)

var (
	db *sql.DB
)

func initDB() {
	var err error
	os.MkdirAll("data", 0755)
	db, err = sql.Open("sqlite", "data/ya_search.db")
	if err != nil {
		slog.Error(fmt.Sprintf("Failed to open SQLite: %v", err))
		os.Exit(1)
	}

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS pages (
			url TEXT PRIMARY KEY,
			title TEXT,
			description TEXT,
			last_crawled DATETIME
		)
	`)
	if err != nil {
		slog.Error(fmt.Sprintf("Failed to create tables: %v", err))
		os.Exit(1)
	}

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS queue (
			url TEXT PRIMARY KEY
		)
	`)
	if err != nil {
		slog.Error(fmt.Sprintf("Failed to create tables: %v", err))
		os.Exit(1)
	}
}

func enqueue(u string) {
	// Only index zypo:// domains
	if !strings.HasPrefix(u, "zypo://") {
		return
	}
	// Strip fragments
	if idx := strings.Index(u, "#"); idx != -1 {
		u = u[:idx]
	}
	// Normalize trailing slash for root
	parsed, err := url.Parse(u)
	if err == nil && parsed.Path == "" {
		u = u + "/"
	}

	_, _ = db.Exec("INSERT OR IGNORE INTO queue (url) VALUES (?)", u)
}

func dequeue() string {
	var u string
	err := db.QueryRow("SELECT url FROM queue LIMIT 1").Scan(&u)
	if err != nil {
		return ""
	}
	db.Exec("DELETE FROM queue WHERE url = ?", u)
	return u
}

func StartCrawler(n *node.ZypoNode) {
	initDB()

	// Seed queue if empty
	var count int
	db.QueryRow("SELECT COUNT(*) FROM queue").Scan(&count)
	if count == 0 {
		enqueue("zypo://domains.zypo/")
		enqueue("zypo://ya.rus/")
		enqueue("zypo://gov.zypo/")
	}

	go func() {
		slog.Info("[Crawler] Spider routine started")
		for {
			u := dequeue()
			if u == "" {
				// Queue empty, wait a bit
				time.Sleep(10 * time.Second)
				continue
			}

			slog.Info(fmt.Sprintf("[Crawler] Crawling %s", u))
			crawlURL(n, u)
			time.Sleep(5 * time.Second) // Be polite
		}
	}()
}

func crawlURL(n *node.ZypoNode, u string) {
	parsed, err := url.Parse(u)
	if err != nil {
		return
	}
	domain := parsed.Host
	path := parsed.Path
	if path == "" {
		path = "/"
	}
	if parsed.RawQuery != "" {
		path += "?" + parsed.RawQuery
	}

	pids, err := n.ResolveDomain(domain)
	if err != nil || len(pids) == 0 {
		slog.Info(fmt.Sprintf("[Crawler] Failed to resolve %s: %v", domain, err))
		return
	}

	// Try fetching from the first available peer
	var bodyReader io.Reader
	var success bool
	for _, pid := range pids {
		body, err := fetchFromPeer(n, pid, domain, path)
		if err == nil {
			bodyReader = bytes.NewReader(body)
			success = true
			break
		}
	}

	if !success {
		slog.Info(fmt.Sprintf("[Crawler] Could not fetch %s from any peer", u))
		return
	}

	// Parse HTML
	doc, err := html.Parse(bodyReader)
	if err != nil {
		return
	}

	title, desc := extractMetadata(doc)
	extractLinks(doc, parsed)

	// Keep it clean
	if len(desc) > 200 {
		desc = desc[:197] + "..."
	}

	_, err = db.Exec(`
		INSERT INTO pages (url, title, description, last_crawled)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(url) DO UPDATE SET
			title=excluded.title,
			description=excluded.description,
			last_crawled=excluded.last_crawled
	`, u, title, desc, time.Now())

	if err != nil {
		slog.Info(fmt.Sprintf("[Crawler] DB insert failed for %s: %v", u, err))
	} else {
		slog.Info(fmt.Sprintf("[Crawler] Successfully indexed %s", u))
	}
}

func fetchFromPeer(n *node.ZypoNode, target peer.ID, domain, path string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(n.GetContext(), 15*time.Second)
	defer cancel()

	s, err := n.Host.NewStream(ctx, target, node.ZypoProtocolID)
	if err != nil {
		return nil, err
	}
	defer s.Close()

	s.SetDeadline(time.Now().Add(10 * time.Second))

	req := node.ZypoRequest{
		Action:   "fetch",
		Resource: domain + path,
	}
	if err := json.NewEncoder(s).Encode(req); err != nil {
		return nil, err
	}

	reader := bufio.NewReader(s)
	line, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}

	var h node.ZypoHeader
	if err := json.Unmarshal([]byte(line), &h); err != nil {
		return nil, err
	}

	if h.Status != 200 {
		return nil, err
	}

	// Only index HTML
	if !strings.Contains(h.Mime, "text/html") {
		return nil, err
	}

	// Limit to 10MB to prevent OOM
	return io.ReadAll(io.LimitReader(reader, 10*1024*1024))
}

func extractMetadata(n *html.Node) (title string, desc string) {
	var f func(*html.Node)
	var inTitle bool
	var inParagraph bool

	var pTextBuilder strings.Builder

	f = func(n *html.Node) {
		// Look for <meta name="description" content="...">
		if n.Type == html.ElementNode && n.Data == "meta" {
			var isDesc bool
			var content string
			for _, a := range n.Attr {
				if a.Key == "name" && strings.ToLower(a.Val) == "description" {
					isDesc = true
				}
				if a.Key == "content" {
					content = a.Val
				}
			}
			if isDesc && content != "" {
				desc = content
			}
		}

		if n.Type == html.ElementNode && n.Data == "title" {
			inTitle = true
		} else if n.Type == html.ElementNode && n.Data == "p" {
			inParagraph = true
		} else if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style") {
			return
		}

		if n.Type == html.TextNode {
			txt := strings.TrimSpace(n.Data)
			if txt != "" {
				if inTitle && title == "" {
					title = txt
				} else if inParagraph && pTextBuilder.Len() < 300 {
					pTextBuilder.WriteString(txt + " ")
				}
			}
		}

		for c := n.FirstChild; c != nil; c = c.NextSibling {
			f(c)
		}

		if n.Type == html.ElementNode && n.Data == "title" {
			inTitle = false
		} else if n.Type == html.ElementNode && n.Data == "p" {
			inParagraph = false
		}
	}
	f(n)

	if desc == "" {
		desc = strings.TrimSpace(pTextBuilder.String())
	}
	return title, desc
}

func extractLinks(n *html.Node, base *url.URL) {
	var f func(*html.Node)
	f = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			for _, a := range n.Attr {
				if a.Key == "href" {
					link := a.Val
					if strings.HasPrefix(link, "zypo://") {
						enqueue(link)
					} else if strings.HasPrefix(link, "/") || (!strings.HasPrefix(link, "http") && !strings.HasPrefix(link, "mailto:")) {
						// Resolve relative link
						parsedLink, err := url.Parse(link)
						if err == nil {
							resolved := base.ResolveReference(parsedLink)
							enqueue(resolved.String())
						}
					}
					break
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			f(c)
		}
	}
	f(n)
}
