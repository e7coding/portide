// Package wire 在 QUIC 流上收发长度前缀的 Protobuf 消息。
package wire

import (
	"encoding/binary"
	"fmt"
	"io"

	"google.golang.org/protobuf/proto"
)

// MaxMessageSize 是单条消息的上限，防止对端用超大长度耗尽内存（设计方案 9 协议解析攻击）。
const MaxMessageSize = 4 << 20

// Write 写入 4 字节大端长度 + 消息体。
func Write(w io.Writer, m proto.Message) error {
	body, err := proto.MarshalOptions{Deterministic: true}.Marshal(m)
	if err != nil {
		return err
	}
	if len(body) > MaxMessageSize {
		return fmt.Errorf("wire: message of %d bytes exceeds limit", len(body))
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(body)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err = w.Write(body)
	return err
}

// Read 读取一条消息到 m。
func Read(r io.Reader, m proto.Message) error { return ReadMax(r, m, MaxMessageSize) }

// ReadMax 与 Read 相同，但拒绝超过 limit 字节的消息。用于对方身份确认之前的消息：
// 外人可以伪造一个很大的长度头，逼本机先分配大块内存。
func ReadMax(r io.Reader, m proto.Message, limit int) error {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > uint32(limit) {
		return fmt.Errorf("wire: incoming message of %d bytes exceeds limit", n)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return err
	}
	return proto.Unmarshal(body, m)
}
