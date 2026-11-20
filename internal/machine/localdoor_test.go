package machine

import (
	"testing"
	"time"
)

func TestDirectIPSnapshotCachedAndReflectsPortChanges(t *testing.T) {
	n := &Machine{}
	// Pre-seed a fresh snapshot so the test is deterministic even on hosts with
	// no non-loopback interfaces.
	seededAt := time.Now()
	n.directIPs = directIPSnapshot{
		at:       seededAt,
		all:      []string{"192.168.1.10", "2408::1"},
		ipv4:     []string{"192.168.1.10"},
		globalV6: []string{"2408::1"},
	}
	n.directPort.Store(41001)
	n.directQUICPort.Store(41002)

	tcpAddrs, quicAddrs := n.directStatusAddrs()
	if len(tcpAddrs) != 2 || tcpAddrs[0] != "192.168.1.10:41001" || tcpAddrs[1] != "[2408::1]:41001" {
		t.Fatalf("unexpected tcpAddrs: %v", tcpAddrs)
	}
	if len(quicAddrs) != 1 || quicAddrs[0] != "192.168.1.10:41002" {
		t.Fatalf("unexpected quicAddrs: %v", quicAddrs)
	}
	if !n.directIPs.at.Equal(seededAt) {
		t.Fatal("directStatusAddrs unexpectedly re-scanned interfaces within cache TTL")
	}

	// Port updates must be reflected immediately without waiting for TTL expiry.
	n.directPort.Store(42001)
	n.directQUICPort.Store(42002)
	tcpAddrs, quicAddrs = n.directStatusAddrs()
	if len(tcpAddrs) != 2 || tcpAddrs[0] != "192.168.1.10:42001" || tcpAddrs[1] != "[2408::1]:42001" {
		t.Fatalf("updated tcpAddrs: %v", tcpAddrs)
	}
	if len(quicAddrs) != 1 || quicAddrs[0] != "192.168.1.10:42002" {
		t.Fatalf("updated quicAddrs: %v", quicAddrs)
	}
	if !n.directIPs.at.Equal(seededAt) {
		t.Fatal("port update unexpectedly re-scanned interfaces within cache TTL")
	}

	// Expired snapshot must refresh.
	n.directIPs.at = time.Now().Add(-directIPCacheTTL - time.Second)
	snap, refreshed := n.cachedDirectIPs(false)
	if !refreshed || snap.at.Before(seededAt) {
		t.Fatalf("expected expired cache to refresh: refreshed=%v at=%v", refreshed, snap.at)
	}
}
