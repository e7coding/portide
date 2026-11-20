package wire

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"

	"portide/core/pb"
)

func TestRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	in := &pb.Hello{Version: 1, DeviceName: "笔记本", Chain: [][]byte{[]byte("r0"), []byte("r1")}}
	if err := Write(&buf, in); err != nil {
		t.Fatal(err)
	}
	if err := Write(&buf, &pb.PairConfirm{InitiatorTag: []byte("tag")}); err != nil {
		t.Fatal(err)
	}
	var out pb.Hello
	if err := Read(&buf, &out); err != nil {
		t.Fatal(err)
	}
	if out.DeviceName != "笔记本" || len(out.Chain) != 2 {
		t.Fatalf("unexpected message %+v", &out)
	}
	var second pb.PairConfirm
	if err := Read(&buf, &second); err != nil || string(second.InitiatorTag) != "tag" {
		t.Fatalf("second message: %v %+v", err, &second)
	}
}

func TestRejectsOversizedLength(t *testing.T) {
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], MaxMessageSize+1)
	err := Read(bytes.NewReader(hdr[:]), &pb.Hello{})
	if err == nil || !strings.Contains(err.Error(), "exceeds limit") {
		t.Fatalf("expected size-limit error, got %v", err)
	}
}

func TestTruncatedBody(t *testing.T) {
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], 10)
	if err := Read(bytes.NewReader(append(hdr[:], 1, 2, 3)), &pb.Hello{}); err == nil {
		t.Fatal("expected error for truncated body")
	}
}

func TestReadMaxRejectsLargerLength(t *testing.T) {
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], 200)
	err := ReadMax(bytes.NewReader(hdr[:]), &pb.Hello{}, 100)
	if err == nil || !strings.Contains(err.Error(), "exceeds limit") {
		t.Fatalf("expected size-limit error, got %v", err)
	}

	var small pb.Hello
	var buf bytes.Buffer
	if err := Write(&buf, &pb.Hello{DeviceName: "ok"}); err != nil {
		t.Fatal(err)
	}
	if err := ReadMax(&buf, &small, 100); err != nil || small.DeviceName != "ok" {
		t.Fatalf("expected success within limit, got %v %+v", err, &small)
	}
}
