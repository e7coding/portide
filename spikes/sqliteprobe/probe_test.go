package sqliteprobe

import (
	"strings"
	"testing"
)

func TestProbePersistsWithWAL(t *testing.T) {
	dir := t.TempDir()
	first, err := Probe(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Log(first)
	for _, want := range []string{"journal=wal", "rows=1000"} {
		if !strings.Contains(first, want) {
			t.Fatalf("first probe %q does not contain %q", first, want)
		}
	}
	second, err := Probe(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(second, "rows=2000") {
		t.Fatalf("second probe %q should see rows from the first run", second)
	}
}
