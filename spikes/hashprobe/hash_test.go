package hashprobe

import (
	"encoding/hex"
	"testing"

	"lukechampine.com/blake3"
)

func TestBlake3EmptyVector(t *testing.T) {
	sum := blake3.Sum256(nil)
	const want = "af1349b9f5f9a1a6a0404dea36dcc9499bcb25c9adc112b7cc9a93cae41f3262"
	if got := hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("BLAKE3(\"\") = %s, want %s", got, want)
	}
}

func TestThroughputReport(t *testing.T) {
	t.Log(Throughput(256))
}
