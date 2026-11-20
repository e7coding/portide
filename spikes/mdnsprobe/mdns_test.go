package mdnsprobe

import (
	"context"
	"os"
	"testing"
	"time"
)

// 需要真实网卡上的组播，CI 或沙箱里通常不可用，所以默认跳过。
func TestAnnounceBrowseSameHost(t *testing.T) {
	if os.Getenv("PORTIDE_MDNS_TEST") != "1" {
		t.Skip("set PORTIDE_MDNS_TEST=1 to run (needs multicast on a real interface)")
	}
	name := Token([]byte("mdns-test-key"), Window(time.Now()), "laptop")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	go func() { _ = Announce(ctx, name, 7443) }()
	got := make(chan Found, 8)
	go func() {
		_ = Browse(ctx, func(f Found) {
			select {
			case got <- f:
			default:
			}
		})
	}()

	for {
		select {
		case f := <-got:
			if f.Name != name {
				continue
			}
			if f.Port != 7443 {
				t.Fatalf("port = %d, want 7443", f.Port)
			}
			t.Logf("discovered %s at %v:%d on %s", f.Name, f.IPs, f.Port, f.Iface)
			return
		case <-ctx.Done():
			t.Fatal("service not discovered within 10s")
		}
	}
}
