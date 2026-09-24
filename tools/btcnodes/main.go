// Command btcnodes queries the BTC Nodes (btcnodes.io) API and prints how
// many reachable Bitcoin nodes on each network support block relay,
// transaction relay and BIP158 compact filters.
//
// Capability mapping (Bitcoin services bitmask):
//
//	block relay:       NODE_NETWORK (1 << 0)
//	transaction relay: NODE_NETWORK (1 << 0) or NODE_NETWORK_LIMITED (1 << 10)
//	compact filters:   NODE_COMPACT_FILTERS (1 << 6)
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"
	"time"
)

const defaultURL = "https://btcnodes.io/api/v1/snapshots/latest/"

// Bitcoin protocol service bits used for the capability counts.
const (
	nodeNetwork        = 1 << 0  // NODE_NETWORK: serves full blocks and relays transactions
	nodeCompactFilters = 1 << 6  // NODE_COMPACT_FILTERS: serves BIP158 compact block filters
	nodeNetworkLimited = 1 << 10 // NODE_NETWORK_LIMITED: serves last 288 blocks, relays transactions
)

// node is one entry of the latest snapshot export. The API encodes each node
// as a 5-field array: protocol version, user agent, connected since,
// services and height.
type node struct {
	protocol       int
	userAgent      string
	connectedSince int64
	services       uint64
	height         int
}

// parseNode decodes the 5-field array representation used by the API.
func parseNode(raw []byte) (node, error) {
	var fields []json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return node{}, err
	}
	if len(fields) < 5 {
		return node{}, fmt.Errorf("node record has %d fields, want 5", len(fields))
	}
	var n node
	if err := json.Unmarshal(fields[0], &n.protocol); err != nil {
		return node{}, fmt.Errorf("protocol: %w", err)
	}
	if err := json.Unmarshal(fields[1], &n.userAgent); err != nil {
		return node{}, fmt.Errorf("user agent: %w", err)
	}
	if err := json.Unmarshal(fields[2], &n.connectedSince); err != nil {
		return node{}, fmt.Errorf("connected since: %w", err)
	}
	if err := json.Unmarshal(fields[3], &n.services); err != nil {
		return node{}, fmt.Errorf("services: %w", err)
	}
	if err := json.Unmarshal(fields[4], &n.height); err != nil {
		return node{}, fmt.Errorf("height: %w", err)
	}
	return n, nil
}

// network is the transport network of a node address.
type network int

const (
	networkOther network = iota
	networkIP            // regular IPv4/IPv6
	networkI2P
	networkTor
)

func (n network) String() string {
	switch n {
	case networkIP:
		return "Regular IP"
	case networkI2P:
		return "I2P"
	case networkTor:
		return "Tor"
	default:
		return "Other"
	}
}

// splitHostPort splits an API address key into host and port. IPv6 literals
// are bracketed ("[2001:db8::1]:8333"), everything else uses host:port.
func splitHostPort(addr string) (string, string) {
	if strings.HasPrefix(addr, "[") {
		if end := strings.LastIndex(addr, "]"); end >= 0 {
			host := addr[1:end]
			port := addr[end+1:]
			return host, strings.TrimPrefix(port, ":")
		}
	}
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		return addr[:i], addr[i+1:]
	}
	return addr, ""
}

// classifyNetwork maps a host to its transport network. CJDNS overlay
// addresses (fc00::/8) are treated as "other" rather than regular IP.
func classifyNetwork(host string) network {
	switch {
	case strings.HasSuffix(host, ".onion"):
		return networkTor
	case strings.HasSuffix(host, ".i2p"):
		return networkI2P
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return networkOther
	}
	if ip.To4() != nil {
		return networkIP
	}
	if len(ip) == net.IPv6len && ip[0]&0xfe == 0xfc {
		return networkOther // CJDNS
	}
	return networkIP
}

// counters accumulates capability counts for one network.
type counters struct {
	total          int
	blockRelay     int
	transactionRel int
	compactFilters int
}

func (c *counters) add(n node) {
	c.total++
	if n.services&nodeNetwork != 0 {
		c.blockRelay++
	}
	if n.services&nodeNetwork != 0 || n.services&nodeNetworkLimited != 0 {
		c.transactionRel++
	}
	if n.services&nodeCompactFilters != 0 {
		c.compactFilters++
	}
}

// fetchJSON downloads the given URL, retrying briefly when the API asks to
// back off (503 "server busy" or 429 rate limited).
func fetchJSON(url string, timeout time.Duration) ([]byte, error) {
	client := &http.Client{Timeout: timeout}
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "bitfyn-btcnodes/1.0")
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		switch resp.StatusCode {
		case http.StatusOK:
			return body, nil
		case http.StatusTooManyRequests, http.StatusServiceUnavailable:
			lastErr = fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
			delay := 2 * time.Second * time.Duration(attempt)
			if ra := resp.Header.Get("Retry-After"); ra != "" {
				if secs, err := time.ParseDuration(ra + "s"); err == nil {
					delay = secs
				}
			}
			time.Sleep(delay)
		default:
			return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
	}
	return nil, fmt.Errorf("giving up after retries: %w", lastErr)
}

func run(url string) error {
	body, err := fetchJSON(url, 60*time.Second)
	if err != nil {
		return fmt.Errorf("fetch %s: %w", url, err)
	}

	var raw struct {
		Timestamp    int64                      `json:"timestamp"`
		TotalNodes   int                        `json:"total_nodes"`
		LatestHeight int                        `json:"latest_height"`
		Nodes        map[string]json.RawMessage `json:"nodes"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return fmt.Errorf("decode snapshot: %w", err)
	}

	stats := map[network]*counters{
		networkIP:  {},
		networkI2P: {},
		networkTor: {},
	}
	malformed := 0
	skipped := 0
	for addr, rawNode := range raw.Nodes {
		host, _ := splitHostPort(addr)
		netw := classifyNetwork(host)
		if netw == networkOther {
			skipped++
			continue
		}
		n, err := parseNode(rawNode)
		if err != nil {
			malformed++
			continue
		}
		stats[netw].add(n)
	}

	snapshotTime := time.Unix(raw.Timestamp, 0).UTC().Format(time.RFC3339)
	fmt.Printf("BTC Nodes capability report\n")
	fmt.Printf("source:   %s\n", url)
	fmt.Printf("snapshot: %s (height %d, total nodes %d)\n\n", snapshotTime, raw.LatestHeight, raw.TotalNodes)

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "Network\tNodes\tBlock relay\tTransaction relay\tCompact filters")
	for _, netw := range []network{networkIP, networkI2P, networkTor} {
		c := stats[netw]
		fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%d\n",
			netw, c.total, c.blockRelay, c.transactionRel, c.compactFilters)
	}
	if err := w.Flush(); err != nil {
		return err
	}

	if malformed > 0 || skipped > 0 {
		fmt.Printf("\nskipped: %d malformed record(s), %d node(s) on other networks\n", malformed, skipped)
	}
	return nil
}

func main() {
	url := flag.String("url", defaultURL, "snapshot API endpoint to query")
	flag.Parse()
	if err := run(*url); err != nil {
		fmt.Fprintln(os.Stderr, "btcnodes:", err)
		os.Exit(1)
	}
}
