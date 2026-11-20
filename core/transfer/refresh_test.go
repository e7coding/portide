package transfer

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRefresh(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.txt"), filepath.Join(dir, "b.txt")
	must(t, os.WriteFile(a, []byte("aaa"), 0o644))
	must(t, os.WriteFile(b, []byte("bbb"), 0o644))
	var p Plan
	must(t, p.AddPath(a))
	must(t, p.AddPath(b))
	must(t, p.AddText(string(make([]byte, MaxTextBytes+1)), time.Now())) // 内存来源
	o, _ := p.Offer(NewTransferID())

	if changed, err := Refresh(o, p.Sources); err != nil || changed {
		t.Fatalf("unchanged sources: changed=%v err=%v", changed, err)
	}

	must(t, os.WriteFile(b, []byte("bbb, longer now"), 0o644))
	mt := time.Now().Add(-time.Hour).Truncate(time.Second)
	must(t, os.Chtimes(b, mt, mt))
	changed, err := Refresh(o, p.Sources)
	if err != nil || !changed {
		t.Fatalf("modified source: changed=%v err=%v", changed, err)
	}
	if it := o.Items[1]; it.Size != 15 || it.MtimeNs != mt.UnixNano() {
		t.Fatalf("item not refreshed: %+v", it)
	}
	if o.Items[0].Size != 3 {
		t.Fatalf("untouched item changed: %+v", o.Items[0])
	}

	must(t, os.Remove(a))
	var missing *MissingError
	if _, err := Refresh(o, p.Sources); !errors.As(err, &missing) || missing.Path != a {
		t.Fatalf("missing source: %v", err)
	}
}
