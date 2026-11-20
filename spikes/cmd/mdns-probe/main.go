// mdns-probe：M0 跨设备 mDNS 发布/浏览测试工具。
//
//	mdns-probe -mode announce            # 以匿名令牌为实例名发布 _portide._udp
//	mdns-probe -mode browse -for 60s     # 打印浏览到的所有 _portide._udp 实例
package main

import (
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"portide/spikes/mdnsprobe"
)

func main() {
	mode := flag.String("mode", "browse", "announce | browse")
	port := flag.Int("port", 7443, "announce 时发布的端口")
	dur := flag.Duration("for", 60*time.Second, "运行时长")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *dur)
	defer cancel()

	switch *mode {
	case "announce":
		key := make([]byte, 32)
		_, _ = rand.Read(key)
		host, _ := os.Hostname()
		name := mdnsprobe.Token(key, mdnsprobe.Window(time.Now()), host)
		fmt.Printf("announcing %s.%s port %d for %s\n", name, mdnsprobe.ServiceType, *port, *dur)
		if err := mdnsprobe.Announce(ctx, name, *port); err != nil && ctx.Err() == nil {
			log.Fatal(err)
		}
	case "browse":
		fmt.Printf("browsing %s for %s\n", mdnsprobe.ServiceType, *dur)
		err := mdnsprobe.Browse(ctx, func(f mdnsprobe.Found) {
			fmt.Printf("found name=%s ips=%v port=%d iface=%s\n", f.Name, f.IPs, f.Port, f.Iface)
		})
		if err != nil && ctx.Err() == nil {
			log.Fatal(err)
		}
	default:
		log.Fatalf("unknown -mode %q", *mode)
	}
}
