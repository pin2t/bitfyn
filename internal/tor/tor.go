// Package tor runs the Tor client the wallet reaches onion peers through. It
// is the pure Go client of github.com/n0madic/go-tor-client compiled into the
// wallet, so no Tor daemon or proxy is needed.
package tor

import "context"
import "fmt"
import "log"
import "log/slog"
import "net"
import "path/filepath"
import "strings"
import "sync"
import "time"
import gotor "github.com/n0madic/go-tor-client"
import "bitfyn/internal/p2p"

// retryDelay is the first wait after a failed bootstrap, doubled after each
// further failure up to maxRetryDelay.
const retryDelay = 5 * time.Second
const maxRetryDelay = 2 * time.Minute

// Client is a Tor client bootstrapping in the background. Its DialContext
// waits until the client is ready.
type Client struct {
	ready  chan struct{}
	stop   chan struct{}
	done   chan struct{}
	cancel context.CancelFunc
	once   sync.Once
	mu     sync.Mutex
	client *gotor.Client
}

// Start bootstraps a Tor client in the background, keeping its consensus
// cache and entry guard under dataDir, and retries with a growing delay
// until it succeeds or the client is closed. Every state change is reported
// through onState, from the bootstrap goroutine, with the error of a failed
// attempt.
func Start(dataDir string, onState func(p2p.State, error)) *Client {
	var ctx, cancel = context.WithCancel(context.Background())
	var c = &Client{
		ready:  make(chan struct{}),
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
		cancel: cancel,
	}
	go c.bootstrap(ctx, dataDir, onState)
	return c
}

// bootstrap connects to the Tor network until it succeeds or ctx ends.
func (c *Client) bootstrap(ctx context.Context, dataDir string, onState func(p2p.State, error)) {
	defer close(c.done)
	var cfg = &gotor.Config{
		DataDir: filepath.Join(dataDir, "tor"),
		Logger:  slog.New(slog.NewTextHandler(logWriter{}, &slog.HandlerOptions{Level: slog.LevelInfo})),
	}
	var delay = retryDelay
	for {
		onState(p2p.StateStarting, nil)
		var started = time.Now()
		var client, err = gotor.NewClient(ctx, cfg)
		if err == nil {
			c.mu.Lock()
			c.client = client
			c.mu.Unlock()
			log.Printf("tor: ready in %s", time.Since(started).Round(time.Millisecond))
			close(c.ready)
			onState(p2p.StateReady, nil)
			return
		}
		if ctx.Err() != nil { return }
		log.Printf("tor: bootstrap failed: %v; retrying in %s", err, delay)
		onState(p2p.StateFailed, err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		delay = min(delay*2, maxRetryDelay)
	}
}

// DialContext opens a stream through Tor to the host:port address, an onion
// service or an internet host, once the client is ready. It gives up when the
// context ends or the client is closed.
func (c *Client) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	select {
	case <-c.ready:
	case <-c.stop:
		return nil, net.ErrClosed
	case <-ctx.Done():
		return nil, fmt.Errorf("tor not ready: %w", ctx.Err())
	}
	c.mu.Lock()
	var client = c.client
	c.mu.Unlock()
	return client.DialContext(ctx, network, address)
}

// Close stops the bootstrap and closes the client with its circuits and
// streams. It is safe to call more than once.
func (c *Client) Close() {
	c.once.Do(func() {
		close(c.stop)
		c.cancel()
		<-c.done
		c.mu.Lock()
		var client = c.client
		c.mu.Unlock()
		if client != nil { _ = client.Close() }
	})
}

// logWriter sends the Tor client's log lines to the wallet log.
type logWriter struct{}

func (logWriter) Write(p []byte) (int, error) {
	log.Printf("tor: %s", strings.TrimSpace(string(p)))
	return len(p), nil
}
