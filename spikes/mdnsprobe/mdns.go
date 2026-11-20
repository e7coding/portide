package mdnsprobe

import (
	"context"
	"net"

	"github.com/brutella/dnssd"
)

// Announce 以 name 为实例名发布 _portide._udp 服务，直到 ctx 结束。TXT 中只放协议版本，不含任何身份信息。
func Announce(ctx context.Context, name string, port int) error {
	srv, err := dnssd.NewService(dnssd.Config{
		Name: name,
		Type: ServiceType,
		Port: port,
		Text: map[string]string{"v": "1"},
		// Host 留空时 dnssd 会回退到真实的 OS 主机名填充 SRV/A/AAAA 记录，
		// 泄露设备身份；显式设为匿名令牌 name，保证外部只能看到轮换的随机串。
		Host: name,
	})
	if err != nil {
		return err
	}
	rp, err := dnssd.NewResponder()
	if err != nil {
		return err
	}
	if _, err := rp.Add(srv); err != nil {
		return err
	}
	return rp.Respond(ctx)
}

// Found 是浏览到的一个服务实例。
type Found struct {
	Name  string
	IPs   []net.IP
	Port  int
	Iface string
}

// Browse 浏览 _portide._udp 服务，每发现一个实例调用一次 found，直到 ctx 结束。
func Browse(ctx context.Context, found func(Found)) error {
	add := func(e dnssd.BrowseEntry) {
		found(Found{Name: e.Name, IPs: e.IPs, Port: e.Port, Iface: e.IfaceName})
	}
	rmv := func(dnssd.BrowseEntry) {}
	return dnssd.LookupType(ctx, ServiceType+".local.", add, rmv)
}
