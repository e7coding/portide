package mobile

import (
	"net"
	"strings"
	"testing"
)

func TestFacadeEndToEnd(t *testing.T) {
	addr, err := StartQuicServer(0)
	if err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	res, err := QuicEcho("127.0.0.1:"+port, 8)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res, "echo 8 MiB") {
		t.Fatalf("unexpected echo result %q", res)
	}
	t.Log(res)

	db, err := SQLiteProbe(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Log(db)

	pk, err := PakeSelfTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Log(pk, "|", HashThroughput(16), "|", MemReport())
}
