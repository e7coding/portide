package app

import "testing"

func TestGuessIcon(t *testing.T) {
	for name, want := range map[string]string{
		"我的手机": IconSmartphone, "iPhone 15": IconSmartphone, "Pixel 8": IconSmartphone,
		"iPad Air": IconTablet, "平板": IconTablet,
		"台式机": IconMonitor, "Office-PC": IconMonitor, "家里的 NAS": IconMonitor, "某设备": IconMonitor,
		"笔记本": IconLaptop, "MacBook": IconLaptop, "joy-ThinkPad": IconLaptop, "游戏笔记本PC": IconLaptop,
	} {
		if got := guessIcon(name); got != want {
			t.Errorf("guessIcon(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestDeviceIconAndRename(t *testing.T) {
	useFileKeystore(t)
	s := runningService(t, "笔记本")
	self := keyOf(t, s)
	icon := func() string {
		devs, err := s.Devices()
		if err != nil {
			t.Fatal(err)
		}
		return devs[0].Icon
	}
	if icon() != IconLaptop {
		t.Fatalf("guessed icon = %q", icon())
	}
	if err := s.SetDeviceIcon(self, IconMonitor); err != nil || icon() != IconMonitor {
		t.Fatalf("SetDeviceIcon: %v, icon = %q", err, icon())
	}
	if err := s.SetDeviceIcon(self, "toaster"); err == nil {
		t.Fatal("an unknown icon must be rejected")
	}
	if err := s.SetDeviceIcon(self, ""); err != nil || icon() != IconLaptop {
		t.Fatalf("reset icon: %v, icon = %q", err, icon())
	}

	if c, err := s.CanRename(); err != nil || !c.Allowed || c.Missing == nil || len(c.Missing) != 0 {
		t.Fatalf("CanRename alone = %+v %v", c, err)
	}
	if err := s.RenameDevice(self, "  新笔记本 "); err != nil {
		t.Fatal(err)
	}
	if st := s.State(); st.Name != "新笔记本" {
		t.Fatalf("name after rename = %q", st.Name)
	}
	if err := s.RenameDevice(self, " "); err == nil || err.Error() != "请填写设备名" {
		t.Fatalf("blank name: %v", err)
	}
}
