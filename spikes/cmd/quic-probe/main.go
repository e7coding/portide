// quic-probe：M0 跨设备 QUIC 回显测试工具。
//
//	quic-probe -mode server -addr :7443
//	quic-probe -mode client -addr 192.168.1.20:7443 -mb 64
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"portide/spikes/quicpin"
)

func main() {
	mode := flag.String("mode", "server", "server | client")
	addr := flag.String("addr", ":7443", "监听地址（server）或目标 host:port（client）")
	allow := flag.String("allow", "", "只接受这个对端 KeyID；留空则接受任何对端并打印其 KeyID")
	mib := flag.Int("mb", 64, "client 回显数据量（MiB）")
	flag.Parse()

	id, err := quicpin.NewIdentity()
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("my KeyID:", id.KeyID)

	af := quicpin.AllowOnly(*allow)
	if *allow == "" {
		af = quicpin.AllowAny(func(k string) { fmt.Println("peer KeyID:", k) })
	}

	switch *mode {
	case "server":
		ln, err := quicpin.Listen(*addr, id, af)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println("listening on", ln.Addr())
		log.Fatal(quicpin.ServeEcho(context.Background(), ln))
	case "client":
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		conn, err := quicpin.Dial(ctx, *addr, id, af)
		if err != nil {
			log.Fatal(err)
		}
		res, err := quicpin.EchoProbe(ctx, conn, *mib)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(res)
		_ = conn.CloseWithError(0, "")
	default:
		log.Fatalf("unknown -mode %q", *mode)
	}
}
