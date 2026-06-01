package node

import (
	"encoding/json"
	"log"
	"net/http"
	"time"
)

func fetchBootstrapFromHTTP(urls []string) []string {
	var allNodes []string
	client := http.Client{Timeout: 5 * time.Second}

	for _, url := range urls {
		resp, err := client.Get(url)
		if err != nil {
			log.Printf("Discovery: Failed to fetch from %s: %v", url, err)
			continue
		}

		var nodes []string
		// Use a temporary interface to check type
		var temp interface{}
		decodeErr := json.NewDecoder(resp.Body).Decode(&temp)
		resp.Body.Close() // Close immediately after reading, not deferred
		if decodeErr != nil {
			log.Printf("Discovery: Failed to decode from %s: %v", url, decodeErr)
			continue
		}

		if slice, ok := temp.([]interface{}); ok {
			for _, item := range slice {
				if str, ok := item.(string); ok {
					nodes = append(nodes, str)
				}
			}
		} else {
			log.Printf("Discovery: URL %s returned non-array data", url)
			continue
		}
		allNodes = append(allNodes, nodes...)
	}

	return allNodes
}
