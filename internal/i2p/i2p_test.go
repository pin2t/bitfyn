package i2p

import "bufio"
import "context"
import "errors"
import "io"
import "net"
import "strings"
import "sync"
import "testing"
import "time"
import "bitfyn/internal/p2p"

// peerName is the b32 name the fake bridge resolves; any other is unknown.
const peerName = "27d5mqzrz34r7kgi44svqyz3sacdtd5abcpwnwrrtdqqhuwefcia.b32.i2p"

// fakeSAM is a SAM 3.1 bridge that resolves peerName, connects streams to
// it, which greet with "hello" right after the connect reply and then echo,
// and can drop the session's control connection.
type fakeSAM struct {
	addr     string
	mu       sync.Mutex
	controls []net.Conn
	sessions []string
}

func newFakeSAM(t *testing.T) *fakeSAM {
	t.Helper()
	var l, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil { t.Fatalf("listen: %v", err) }
	t.Cleanup(func() { l.Close() })
	var f = &fakeSAM{addr: l.Addr().String()}
	go func() {
		for {
			var c, err = l.Accept()
			if err != nil { return }
			t.Cleanup(func() { c.Close() })
			go f.serve(c)
		}
	}()
	return f
}

func (f *fakeSAM) serve(c net.Conn) {
	var r = bufio.NewReader(c)
	for {
		var line, err = r.ReadString('\n')
		if err != nil { return }
		var fields = strings.Fields(line)
		switch {
		case strings.HasPrefix(line, "HELLO VERSION"):
			io.WriteString(c, "HELLO REPLY RESULT=OK VERSION=3.1\n")
		case strings.HasPrefix(line, "SESSION CREATE"):
			f.mu.Lock()
			f.controls = append(f.controls, c)
			f.sessions = append(f.sessions, strings.TrimPrefix(fields[3], "ID="))
			f.mu.Unlock()
			io.WriteString(c, "SESSION STATUS RESULT=OK DESTINATION=privkey==\n")
		case strings.HasPrefix(line, "NAMING LOOKUP"):
			var name = strings.TrimPrefix(fields[2], "NAME=")
			if name != peerName {
				io.WriteString(c, "NAMING REPLY RESULT=KEY_NOT_FOUND NAME="+name+"\n")
				continue
			}
			io.WriteString(c, "NAMING REPLY RESULT=OK NAME="+name+" VALUE=dest~AAAA==\n")
		case strings.HasPrefix(line, "STREAM CONNECT"):
			if fields[3] != "DESTINATION=dest~AAAA==" {
				io.WriteString(c, "STREAM STATUS RESULT=CANT_REACH_PEER MESSAGE=\"no such peer\"\n")
				continue
			}
			io.WriteString(c, "STREAM STATUS RESULT=OK\nhello")
			io.Copy(c, r)
			return
		}
	}
}

// dropSession closes the control connection of the last session.
func (f *fakeSAM) dropSession() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.controls[len(f.controls)-1].Close()
}

func (f *fakeSAM) sessionCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sessions)
}

// states collects the states a client reports and tells of each ready one.
type states struct {
	ready chan struct{}
}

func (s *states) report(state p2p.State, _ error) {
	if state == p2p.StateReady { s.ready <- struct{}{} }
}

func (s *states) awaitReady(t *testing.T) {
	t.Helper()
	select {
	case <-s.ready:
	case <-time.After(5 * time.Second):
		t.Fatalf("session not ready")
	}
}

// TestDial creates a session, opens a stream to the peer that reads what
// the bridge sent with the connect reply and echoes, and fails to dial an
// unknown peer or a host that is not an I2P name.
func TestDial(t *testing.T) {
	var sam = newFakeSAM(t)
	var s = &states{ready: make(chan struct{}, 4)}
	var c = Start(sam.addr, s.report)
	defer c.Close()
	s.awaitReady(t)
	var ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var conn, err = c.DialContext(ctx, "tcp", peerName+":8333")
	if err != nil { t.Fatalf("DialContext: %v", err) }
	defer conn.Close()
	if conn.RemoteAddr().String() != peerName {
		t.Fatalf("remote address %q", conn.RemoteAddr())
	}
	var buf = make([]byte, 5)
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "hello" {
		t.Fatalf("read %q, %v; want hello", buf, err)
	}
	io.WriteString(conn, "ping")
	buf = make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("echo %q, %v", buf, err)
	}
	var unknown = "aaaa" + peerName[4:]
	if _, err := c.DialContext(ctx, "tcp", unknown+":8333"); err == nil || !strings.Contains(err.Error(), "KEY_NOT_FOUND") {
		t.Fatalf("dial of an unknown peer: %v", err)
	}
	if _, err := c.DialContext(ctx, "tcp", "192.0.2.1:8333"); err == nil {
		t.Fatalf("dial of an IP address succeeded")
	}
}

// TestSessionLost drops the session and checks that a new one, with a new
// ID, is created at once and dialed through, and that a closed client dials
// nothing.
func TestSessionLost(t *testing.T) {
	var saved = stableSession
	stableSession = 0
	defer func() { stableSession = saved }()
	var sam = newFakeSAM(t)
	var s = &states{ready: make(chan struct{}, 4)}
	var c = Start(sam.addr, s.report)
	s.awaitReady(t)
	sam.dropSession()
	s.awaitReady(t)
	if n := sam.sessionCount(); n != 2 || sam.sessions[0] == sam.sessions[1] {
		t.Fatalf("sessions %v, want two with different IDs", sam.sessions)
	}
	var ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var conn, err = c.DialContext(ctx, "tcp", peerName+":0")
	if err != nil { t.Fatalf("DialContext on the new session: %v", err) }
	conn.Close()
	c.Close()
	if _, err := c.DialContext(ctx, "tcp", peerName+":0"); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("dial after Close = %v, want net.ErrClosed", err)
	}
}

// TestNoRouter checks that a missing router is reported as a failure and
// that a dial waiting for the session gives up with its context.
func TestNoRouter(t *testing.T) {
	var l, _ = net.Listen("tcp", "127.0.0.1:0")
	var addr = l.Addr().String()
	l.Close()
	var failed = make(chan error, 4)
	var c = Start(addr, func(state p2p.State, err error) {
		if state == p2p.StateFailed { failed <- err }
	})
	defer c.Close()
	select {
	case err := <-failed:
		if !strings.Contains(err.Error(), "no I2P router SAM bridge") {
			t.Fatalf("failure %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("no failure reported")
	}
	var ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := c.DialContext(ctx, "tcp", peerName+":0"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("dial without a session = %v, want the context deadline", err)
	}
}

// TestParseReply reads plain, quoted and value-less fields.
func TestParseReply(t *testing.T) {
	var got = parseReply(`STREAM STATUS RESULT=CANT_REACH_PEER MESSAGE="peer not found" VALUE=ab~c==` + "\n")
	var want = map[string]string{"STREAM": "", "STATUS": "", "RESULT": "CANT_REACH_PEER", "MESSAGE": "peer not found", "VALUE": "ab~c=="}
	if len(got) != len(want) {
		t.Fatalf("parseReply = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("parseReply[%s] = %q, want %q", k, got[k], v)
		}
	}
}
