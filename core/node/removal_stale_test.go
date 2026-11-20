package node

import "testing"

func TestRemovalMayBeStale(t *testing.T) {
	other := DeviceStatus{KeyID: "other", Online: true}
	self := DeviceStatus{KeyID: "self", Self: true, Online: true}
	target := DeviceStatus{KeyID: "target", Online: true}
	for _, c := range []struct {
		name          string
		devs          []DeviceStatus
		daemonRunning bool
		target        string
		want          bool
	}{
		{"守护进程未运行", []DeviceStatus{self, other}, false, "other", true},
		{"守护进程在运行，只有本机", []DeviceStatus{self}, true, "", true},
		{"守护进程在运行，另一台设备在线", []DeviceStatus{self, other}, true, "", false},
		{"守护进程在运行，只有移除目标在线", []DeviceStatus{self, target}, true, "target", true},
		{"守护进程在运行，移除目标和另一台设备都在线", []DeviceStatus{self, target, other}, true, "target", false},
	} {
		if got := RemovalMayBeStale(c.devs, c.daemonRunning, c.target); got != c.want {
			t.Errorf("%s: RemovalMayBeStale = %v, want %v", c.name, got, c.want)
		}
	}
}
