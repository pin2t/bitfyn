package sync

import "fmt"
import "log"
import "time"
import "github.com/btcsuite/btcd/chaincfg/chainhash"
import "github.com/btcsuite/btcd/peer"
import "github.com/btcsuite/btcd/wire"
import "bitfyn/internal/p2p"

// conn is one peer connection with its own response channels, so several
// peers can be queried at once without mixing their answers.
type conn struct {
	peer      *peer.Peer
	addr      string
	host      string
	port      uint16
	quit      chan struct{}
	headers   chan *wire.MsgHeaders
	cfheaders chan *wire.MsgCFHeaders
	cfilters  chan *wire.MsgCFilter
	blocks    chan *wire.MsgBlock
	notFound  chan *wire.MsgNotFound
}

// newConn prepares a connection to the host:port address.
func newConn(addr string) (*conn, error) {
	var host, port, err = splitHostPort(addr)
	if err != nil { return nil, err }
	return &conn{
		addr:      addr,
		host:      host,
		port:      port,
		quit:      make(chan struct{}),
		headers:   make(chan *wire.MsgHeaders, 4),
		cfheaders: make(chan *wire.MsgCFHeaders, 4),
		cfilters:  make(chan *wire.MsgCFilter, filterBatch),
		blocks:    make(chan *wire.MsgBlock, 2),
		notFound:  make(chan *wire.MsgNotFound, 2),
	}, nil
}

// dial connects and completes the handshake. quit is closed once the peer
// disconnects, for whatever reason.
func (c *conn) dial() (time.Duration, error) {
	var p, handshake, err = p2p.Dial(params, c.addr, c.listeners())
	if err != nil { return 0, err }
	c.peer = p
	go func() {
		p.WaitForDisconnect()
		close(c.quit)
	}()
	return handshake, nil
}

// close disconnects the peer and waits until it is gone.
func (c *conn) close() {
	c.peer.Disconnect()
	<-c.quit
}

func (c *conn) live() bool {
	select {
	case <-c.quit:
		return false
	default:
		return true
	}
}

// drop disconnects a peer that failed or misbehaved and logs why.
func (c *conn) drop(reason string, err error) {
	log.Printf("peer %s: %s: %v; disconnecting", c.addr, reason, err)
	c.peer.Disconnect()
}

// record stores the outcome and latency of one request against the peer.
func (c *conn) record(ok bool, started time.Time) {
	if err := store.RecordPeerResult(c.host, c.port, ok, time.Since(started).Milliseconds()); err != nil {
		log.Printf("record peer %s: %v", c.addr, err)
	}
}

// listeners routes the peer's responses to this connection's channels.
// Advertised peer addresses are persisted, and a block announcement wakes the
// sync loop.
func (c *conn) listeners() peer.MessageListeners {
	return peer.MessageListeners{
		OnHeaders:   func(_ *peer.Peer, msg *wire.MsgHeaders) { deliver(c, c.headers, msg) },
		OnCFHeaders: func(_ *peer.Peer, msg *wire.MsgCFHeaders) { deliver(c, c.cfheaders, msg) },
		OnCFilter:   func(_ *peer.Peer, msg *wire.MsgCFilter) { deliver(c, c.cfilters, msg) },
		OnBlock:     func(_ *peer.Peer, msg *wire.MsgBlock, _ []byte) { deliver(c, c.blocks, msg) },
		OnNotFound:  func(_ *peer.Peer, msg *wire.MsgNotFound) { deliver(c, c.notFound, msg) },
		OnInv: func(_ *peer.Peer, msg *wire.MsgInv) {
			for _, iv := range msg.InvList {
				if iv.Type != wire.InvTypeBlock { continue }
				if chain != nil && chain.HeightOf(iv.Hash) < 0 {
					log.Printf("peer %s announced block %s", c.addr, iv.Hash)
				}
				wakeSync()
				return
			}
		},
		OnAddr: func(_ *peer.Peer, msg *wire.MsgAddr) {
			for _, na := range msg.AddrList {
				if na.IP == nil || na.Port == 0 { continue }
				var ip = na.IP.String()
				if na.Services != 0 {
					_ = store.UpdatePeerServices(ip, na.Port, uint64(na.Services))
				} else {
					_ = store.SavePeer(ip, na.Port)
				}
			}
		},
	}
}

// deliver hands a message to the waiting request. It gives up once the peer
// disconnects so a stalled channel never blocks the peer's read loop forever.
func deliver[T any](c *conn, ch chan T, msg T) {
	select {
	case ch <- msg:
	case <-c.quit:
	}
}

// await waits for the next message on the channel until the timeout, the
// peer's disconnection or the sync shutdown.
func await[T any](c *conn, ch chan T, timeout time.Duration, what string) (T, error) {
	var zero T
	var timer = time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case msg := <-ch:
		return msg, nil
	case <-c.quit:
		return zero, fmt.Errorf("peer disconnected")
	case <-stop:
		return zero, fmt.Errorf("sync stopped")
	case <-timer.C:
		return zero, fmt.Errorf("no %s response within %s", what, timeout)
	}
}

// drain discards stale responses left by an earlier request.
func drain[T any](ch chan T) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

// getHeaders requests the headers following the block locator.
func (c *conn) getHeaders(locator []*chainhash.Hash) ([]*wire.BlockHeader, error) {
	drain(c.headers)
	var msg = wire.NewMsgGetHeaders()
	msg.ProtocolVersion = c.peer.ProtocolVersion()
	msg.BlockLocatorHashes = locator
	var started = time.Now()
	c.peer.QueueMessage(msg, nil)
	var resp, err = await(c, c.headers, requestTimeout, "headers")
	c.record(err == nil, started)
	if err != nil { return nil, err }
	return resp.Headers, nil
}

// getCFHeaders sends one getcfheaders request and validates the response
// type, stop hash and filter hash count.
func (c *conn) getCFHeaders(start int32, stop chainhash.Hash, timeout time.Duration) (*wire.MsgCFHeaders, error) {
	drain(c.cfheaders)
	var msg = wire.NewMsgGetCFHeaders(wire.GCSFilterRegular, uint32(start), &stop)
	var started = time.Now()
	c.peer.QueueMessage(msg, nil)
	var resp, err = await(c, c.cfheaders, timeout, "cfheaders")
	c.record(err == nil, started)
	if err != nil { return nil, err }
	if resp.FilterType != wire.GCSFilterRegular {
		return nil, fmt.Errorf("unexpected filter type %d", resp.FilterType)
	}
	if resp.StopHash != stop {
		return nil, fmt.Errorf("stop hash %s, want %s", resp.StopHash, stop)
	}
	var wantCount = int(chain.HeightOf(stop) - start + 1)
	if len(resp.FilterHashes) != wantCount {
		return nil, fmt.Errorf("got %d filter headers for %d blocks", len(resp.FilterHashes), wantCount)
	}
	return resp, nil
}

// getCFilters downloads the filters of the blocks start..stop and returns
// them in height order. Every filter must belong to a block of the range and
// arrive once.
func (c *conn) getCFilters(start int32, stop chainhash.Hash) ([][]byte, error) {
	drain(c.cfilters)
	var end = chain.HeightOf(stop)
	var msg = wire.NewMsgGetCFilters(wire.GCSFilterRegular, uint32(start), &stop)
	var started = time.Now()
	c.peer.QueueMessage(msg, nil)
	var out = make([][]byte, end-start+1)
	for range out {
		var resp, err = await(c, c.cfilters, requestTimeout, "cfilter")
		if err != nil {
			c.record(false, started)
			return nil, err
		}
		if resp.FilterType != wire.GCSFilterRegular {
			c.record(false, started)
			return nil, fmt.Errorf("unexpected filter type %d", resp.FilterType)
		}
		var height = chain.HeightOf(resp.BlockHash)
		if height < start || height > end || out[height-start] != nil {
			c.record(false, started)
			return nil, fmt.Errorf("unexpected cfilter for block %s", resp.BlockHash)
		}
		out[height-start] = resp.Data
	}
	c.record(true, started)
	return out, nil
}

// getBlock downloads the full block, with witness data when the peer serves
// it. The block is not verified here.
func (c *conn) getBlock(hash chainhash.Hash) (*wire.MsgBlock, error) {
	drain(c.blocks)
	drain(c.notFound)
	var kind = wire.InvTypeBlock
	if c.peer.Services()&wire.SFNodeWitness != 0 {
		kind = wire.InvTypeWitnessBlock
	}
	var msg = wire.NewMsgGetData()
	if err := msg.AddInvVect(wire.NewInvVect(kind, &hash)); err != nil {
		return nil, err
	}
	var started = time.Now()
	c.peer.QueueMessage(msg, nil)
	var timer = time.NewTimer(requestTimeout)
	defer timer.Stop()
	for {
		select {
		case blk := <-c.blocks:
			if blk.BlockHash() != hash { continue }
			c.record(true, started)
			return blk, nil
		case <-c.notFound:
			c.record(false, started)
			return nil, fmt.Errorf("block %s not found", hash)
		case <-c.quit:
			return nil, fmt.Errorf("peer disconnected")
		case <-stop:
			return nil, fmt.Errorf("sync stopped")
		case <-timer.C:
			c.record(false, started)
			return nil, fmt.Errorf("no block response within %s", requestTimeout)
		}
	}
}

// fanOut runs the request against every peer at once. It returns the peers
// that answered, in their original order, with their results; a peer whose
// request failed is disconnected.
func fanOut[T any](peers []*conn, what string, request func(*conn) (T, error)) ([]*conn, []T) {
	var results = make([]T, len(peers))
	var errs = make([]error, len(peers))
	var done = make(chan struct{}, len(peers))
	for i, c := range peers {
		go func() {
			results[i], errs[i] = request(c)
			done <- struct{}{}
		}()
	}
	for range peers {
		<-done
	}
	var okPeers []*conn
	var okResults []T
	for i, c := range peers {
		if errs[i] != nil {
			c.drop(what, errs[i])
			continue
		}
		okPeers = append(okPeers, c)
		okResults = append(okResults, results[i])
	}
	return okPeers, okResults
}
