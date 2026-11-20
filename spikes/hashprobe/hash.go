// Package hashprobe 测量 BLAKE3（lukechampine.com/blake3）在目标平台上的哈希吞吐。M0 一次性验证代码。
package hashprobe

import (
	"crypto/rand"
	"fmt"
	"time"

	"lukechampine.com/blake3"
)

// Throughput 以 1 MiB 为块流式哈希 mib MiB 数据，返回耗时与吞吐描述。
func Throughput(mib int) string {
	buf := make([]byte, 1<<20)
	_, _ = rand.Read(buf)
	h := blake3.New(32, nil)
	start := time.Now()
	for i := 0; i < mib; i++ {
		_, _ = h.Write(buf)
	}
	_ = h.Sum(nil)
	el := time.Since(start)
	return fmt.Sprintf("blake3 %d MiB in %s (%.0f MiB/s)", mib, el.Round(time.Millisecond), float64(mib)/el.Seconds())
}
