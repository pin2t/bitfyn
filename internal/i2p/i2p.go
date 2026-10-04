// Package i2p reaches I2P peers through the SAM bridge of an I2P router
// running next to the wallet, such as i2pd, as Bitcoin Core does. It keeps
// one SAM stream session with a transient destination and opens a stream
// over it to every peer dialed. SAM 3.1 is spoken, without ports, which is
// what Bitcoin Core's I2P nodes accept.
package i2p

import "bufio"
import "context"
import "crypto/rand"
import "encoding/hex"
import "fmt"
import "log"
import "net"
import "strings"
import "sync"
import "time"
import "bitfyn/internal/p2p"

// DefaultSAM is the address of the SAM bridge of i2pd and Java I2P.
const DefaultSAM = "127.0.0.1:7656"

// retryDelay is the first wait after the session could not be created or
// was lost, doubled after each further failure up to maxRetryDelay.
const retryDelay = 5 * time.Second
const maxRetryDelay = 2 * time.Minute

// stableSession is how long a session must have lasted for a new one to be
// created at once when it is lost; one lost sooner counts as a failure.
var stableSession = time.Minute

// sessionTimeout bounds creating the session, which waits for the router
// to build its tunnels.
const sessionTimeout = 3 * time.Minute

// sessionOptions are the session options Bitcoin Core uses: an Ed25519
// destination with the X25519 and ElGamal lease set encryption, and one
// tunnel each way, which is enough for a few peers.
const sessionOptions = "SIGNATURE_TYPE=7 i2cp.leaseSetEncType=4,0 inbound.quantity=1 outbound.quantity=1"

// Client keeps a SAM stream session with the I2P router and dials peers
// through it. Its DialContext waits until the session is created.
type Client struct {
	sam     string
	stop    chan struct{}
	done    chan struct{}
	once    sync.Once
	mu      sync.Mutex
	ready   chan struct{}
	session string
	control net.Conn
}

// Start creates the session with the router's SAM bridge at sam in the
// background, and creates a new one at once whenever it is lost after
// stableSession, retrying with a growing delay after failures, until the
// client is closed. Every state change is
// reported through onState, from the session goroutine, with the error of a
// failed attempt.
func Start(sam string, onState func(p2p.State, error)) *Client {
	var c = &Client{
		sam:   sam,
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
		ready: make(chan struct{}),
	}
	go c.keepSession(onState)
	return c
}

// keepSession creates the session, holds it while its control connection
// stays open and creates a new one once it is lost.
func (c *Client) keepSession(onState func(p2p.State, error)) {
	defer close(c.done)
	var delay = retryDelay
	for {
		onState(p2p.StateStarting, nil)
		var started = time.Now()
		var control, id, err = c.createSession()
		if err == nil {
			log.Printf("i2p: session %s ready in %s", id, time.Since(started).Round(time.Millisecond))
			c.mu.Lock()
			c.session, c.control = id, control
			close(c.ready)
			c.mu.Unlock()
			onState(p2p.StateReady, nil)
			var readyAt = time.Now()
			var lost = holdSession(control, c.stop)
			c.mu.Lock()
			c.ready = make(chan struct{})
			c.session, c.control = "", nil
			c.mu.Unlock()
			control.Close()
			if !lost { return }
			if time.Since(readyAt) >= stableSession {
				log.Printf("i2p: session %s lost, creating a new one", id)
				delay = retryDelay
				continue
			}
			err = fmt.Errorf("session %s lost after %s", id, time.Since(readyAt).Round(time.Millisecond))
		}
		select {
		case <-c.stop:
			return
		default:
		}
		log.Printf("i2p: %v; retrying in %s", err, delay)
		onState(p2p.StateFailed, err)
		select {
		case <-c.stop:
			return
		case <-time.After(delay):
		}
		delay = min(delay*2, maxRetryDelay)
	}
}

// createSession opens the control connection and creates a stream session
// with a transient destination and a fresh ID on it.
func (c *Client) createSession() (net.Conn, string, error) {
	var ctx, cancel = context.WithTimeout(context.Background(), sessionTimeout)
	defer cancel()
	go func() {
		select {
		case <-c.stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	var conn, reader, err = c.hello(ctx)
	if err != nil { return nil, "", err }
	var id = "bitfyn-" + randomID()
	if _, err := command(ctx, conn, reader, "SESSION CREATE STYLE=STREAM ID="+id+" DESTINATION=TRANSIENT "+sessionOptions); err != nil {
		conn.Close()
		return nil, "", fmt.Errorf("create session: %w", err)
	}
	return conn, id, nil
}

// holdSession waits until the control connection closes, the session being
// lost with it, or the client is closed. It answers the router's pings and
// reports whether the session was lost.
func holdSession(control net.Conn, stop <-chan struct{}) bool {
	var lost = make(chan struct{})
	go func() {
		defer close(lost)
		var reader = bufio.NewReader(control)
		for {
			var line, err = reader.ReadString('\n')
			if err != nil { return }
			if rest, ok := strings.CutPrefix(line, "PING"); ok {
				fmt.Fprintf(control, "PONG%s", rest)
			}
		}
	}()
	select {
	case <-lost:
		return true
	case <-stop:
		return false
	}
}

// DialContext opens a stream to the I2P peer at address, a b32 name with a
// port that is ignored, once the session is ready: it looks the name up and
// connects to the destination. It gives up when the context ends or the
// client is closed.
func (c *Client) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	var host, _, err = net.SplitHostPort(address)
	if err != nil { return nil, err }
	if !p2p.IsI2P(host) { return nil, fmt.Errorf("%q is not an I2P b32 address", host) }
	c.mu.Lock()
	var ready = c.ready
	c.mu.Unlock()
	select {
	case <-ready:
	case <-c.stop:
		return nil, net.ErrClosed
	case <-ctx.Done():
		return nil, fmt.Errorf("i2p not ready: %w", ctx.Err())
	}
	c.mu.Lock()
	var id = c.session
	c.mu.Unlock()
	if id == "" { return nil, fmt.Errorf("i2p session lost") }
	var conn, reader, herr = c.hello(ctx)
	if herr != nil { return nil, herr }
	var fail = func(err error) (net.Conn, error) {
		conn.Close()
		return nil, err
	}
	var reply, lerr = command(ctx, conn, reader, "NAMING LOOKUP NAME="+host)
	if lerr != nil { return fail(fmt.Errorf("look up %s: %w", host, lerr)) }
	var dest = reply["VALUE"]
	if dest == "" { return fail(fmt.Errorf("look up %s: no destination", host)) }
	if _, err := command(ctx, conn, reader, "STREAM CONNECT ID="+id+" DESTINATION="+dest+" SILENT=false"); err != nil {
		return fail(fmt.Errorf("connect %s: %w", host, err))
	}
	return &stream{Conn: conn, reader: reader, remote: i2pAddr(host)}, nil
}

// Close ends the session and stops creating new ones. Streams opened end
// with it. It is safe to call more than once.
func (c *Client) Close() {
	c.once.Do(func() {
		close(c.stop)
		<-c.done
	})
}

// hello connects to the SAM bridge and agrees on SAM 3.1.
func (c *Client) hello(ctx context.Context) (net.Conn, *bufio.Reader, error) {
	var dialer net.Dialer
	var conn, err = dialer.DialContext(ctx, "tcp", c.sam)
	if err != nil { return nil, nil, fmt.Errorf("no I2P router SAM bridge at %s: %w", c.sam, err) }
	var reader = bufio.NewReader(conn)
	if _, err := command(ctx, conn, reader, "HELLO VERSION MIN=3.1 MAX=3.1"); err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("SAM hello: %w", err)
	}
	return conn, reader, nil
}

// command sends one SAM command and reads its reply line, failing unless
// the reply's RESULT is OK. The reply's key=value fields are returned. The
// context bounds the exchange.
func command(ctx context.Context, conn net.Conn, reader *bufio.Reader, line string) (map[string]string, error) {
	var stop = context.AfterFunc(ctx, func() { conn.SetDeadline(time.Now()) })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
		defer conn.SetDeadline(time.Time{})
	}
	if _, err := fmt.Fprintf(conn, "%s\n", line); err != nil {
		return nil, contextErr(ctx, err)
	}
	var reply, err = reader.ReadString('\n')
	if err != nil { return nil, contextErr(ctx, err) }
	var fields = parseReply(reply)
	if fields["RESULT"] != "OK" {
		if msg := fields["MESSAGE"]; msg != "" {
			return fields, fmt.Errorf("%s: %s", fields["RESULT"], msg)
		}
		return fields, fmt.Errorf("%s", strings.TrimSpace(reply))
	}
	return fields, nil
}

// contextErr is the context's error once it ended, err otherwise.
func contextErr(ctx context.Context, err error) error {
	if ctx.Err() != nil { return ctx.Err() }
	return err
}

// parseReply reads the key=value fields of a SAM reply line, a quoted value
// possibly holding spaces.
func parseReply(line string) map[string]string {
	var fields = make(map[string]string)
	line = strings.TrimSpace(line)
	for line != "" {
		var token string
		var key, rest, ok = strings.Cut(line, "=")
		if space := strings.IndexByte(line, ' '); !ok || (space >= 0 && space < len(key)) {
			token, line, _ = strings.Cut(line, " ")
			fields[token] = ""
			line = strings.TrimLeft(line, " ")
			continue
		}
		var value string
		if strings.HasPrefix(rest, `"`) {
			value, line, _ = strings.Cut(rest[1:], `"`)
		} else {
			value, line, _ = strings.Cut(rest, " ")
		}
		fields[key] = value
		line = strings.TrimLeft(line, " ")
	}
	return fields
}

// randomID is 8 random bytes in hex, so a new session never clashes with
// one the router still holds from before.
func randomID() string {
	var b = make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// stream is a connected SAM stream. Data the router sent right after the
// connect reply is already buffered in the reader, so reads go through it.
type stream struct {
	net.Conn
	reader *bufio.Reader
	remote i2pAddr
}

func (s *stream) Read(p []byte) (int, error) { return s.reader.Read(p) }

func (s *stream) RemoteAddr() net.Addr { return s.remote }

// i2pAddr is the b32 address of an I2P peer.
type i2pAddr string

func (a i2pAddr) Network() string { return "i2p" }

func (a i2pAddr) String() string { return string(a) }
