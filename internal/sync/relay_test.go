package sync

import "testing"
import "github.com/btcsuite/btcd/chaincfg"
import "github.com/btcsuite/btcd/peer"
import "github.com/btcsuite/btcd/wire"

// relayConn is an unconnected peer that advertises the services.
func relayConn(t *testing.T, addr string, services wire.ServiceFlag) *conn {
	t.Helper()
	var p, err = peer.NewOutboundPeer(&peer.Config{ChainParams: &chaincfg.RegressionNetParams, Services: services}, addr)
	if err != nil {
		t.Fatalf("NewOutboundPeer: %v", err)
	}
	var c, cerr = newConn(addr)
	if cerr != nil {
		t.Fatalf("newConn: %v", cerr)
	}
	c.peer = p
	return c
}

// TestRelaySource checks that bloom peers are the relay source whenever one
// is connected, and that the primary alone is otherwise.
func TestRelaySource(t *testing.T) {
	var cf = relayConn(t, "10.0.0.1:8333", wire.SFNodeCF)
	var cf2 = relayConn(t, "10.0.0.2:8333", wire.SFNodeCF)
	var bloom = relayConn(t, "10.0.0.3:8333", wire.SFNodeCF|wire.SFNodeBloom)
	var outsider = relayConn(t, "10.0.0.4:8333", wire.SFNodeCF)
	var withBloom = []*conn{cf, bloom, cf2}
	if relaySource(cf, withBloom) || relaySource(cf2, withBloom) || !relaySource(bloom, withBloom) {
		t.Error("with a bloom peer connected, only bloom peers must be relay sources")
	}
	var plain = []*conn{cf, cf2}
	if !relaySource(cf, plain) || relaySource(cf2, plain) || relaySource(outsider, plain) {
		t.Error("without bloom peers, only the primary must be the relay source")
	}
	if relaySource(cf, nil) {
		t.Error("relay source without connected peers")
	}
	if got := describeRelay(withBloom); got != "bloom filters on 1 peer" {
		t.Errorf("describeRelay(with bloom) = %q", got)
	}
	if got := describeRelay(plain); got != "no peer serves bloom filters, downloading all relayed transactions from 10.0.0.1:8333" {
		t.Errorf("describeRelay(plain) = %q", got)
	}
}
