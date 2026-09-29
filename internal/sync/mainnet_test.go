//go:build mainnet

package sync

import "errors"
import "os"
import "path/filepath"
import "testing"
import "time"
import "bitfyn/internal/storage"
import "bitfyn/internal/wallet"

// TestMainnetSync syncs a throwaway mainnet wallet created two days ago
// against the peer in BITFYN_PEER, keeping the database in BITFYN_DIR so a
// rerun resumes.
func TestMainnetSync(t *testing.T) {
	var net, _ = wallet.ParamsForNetwork("mainnet")
	var st, err = storage.Open(filepath.Join(os.Getenv("BITFYN_DIR"), "w.db"), "")
	if err != nil { t.Fatal(err) }
	defer st.Close()
	if _, err := st.Meta(); errors.Is(err, storage.ErrNoWallet) {
		var mnemonic, _ = wallet.NewMnemonic(128)
		var w, _ = wallet.New(mnemonic, "", net)
		var xpub, _ = w.AccountXPub()
		if err := st.SaveMeta(mnemonic, xpub, "mainnet", time.Now().Add(-48*time.Hour).Unix()); err != nil { t.Fatal(err) }
		var addr, path, pub, _ = w.DeriveAddress(0)
		if err := st.AddAddress(0, path, addr, pub); err != nil { t.Fatal(err) }
	}
	var statuses = make(chan Status, 1024)
	var started = time.Now()
	err = Start(net, st, os.Getenv("BITFYN_PEER"), func(s Status) {
		select {
		case statuses <- s:
		default:
		}
	})
	if err != nil { t.Fatal(err) }
	defer Stop()
	var deadline = time.After(55 * time.Minute)
	var lastLine string
	for {
		select {
		case s := <-statuses:
			if line := s.String(); line != lastLine && (s.State == StateIdle || s.Height%20000 == 0) {
				t.Logf("status after %s: bars %d, %q", time.Since(started).Round(time.Second), s.Bars(), line)
				lastLine = line
			}
			if s.State != StateIdle || chain.Height() < 900000 { continue }
			var resume, _ = st.FilterResumeHeight(FilterStart())
			if resume <= chain.Height() { continue }
			var filters, _ = st.FilterCount()
			t.Logf("synced after %s: tip %d, filter start %d, %d filters stored, %d peers",
				time.Since(started).Round(time.Second), chain.Height(), FilterStart(), filters, s.Peers)
			time.Sleep(20 * time.Second)
			return
		case <-deadline:
			t.Fatalf("not synced after %s, tip %d", time.Since(started).Round(time.Second), chain.Height())
		}
	}
}
