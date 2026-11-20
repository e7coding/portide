package mdnsprobe

import (
	"regexp"
	"testing"
	"time"
)

var (
	key   = []byte("discovery-key-for-tests-32-bytes")
	now   = time.Unix(1_800_000_000, 0)
	hex24 = regexp.MustCompile(`^[0-9a-f]{24}$`)
)

func TestTokenShape(t *testing.T) {
	tok := Token(key, Window(now), "laptop")
	if !hex24.MatchString(tok) {
		t.Fatalf("token %q is not 24 lowercase hex chars", tok)
	}
	if tok != Token(key, Window(now), "laptop") {
		t.Fatal("token must be deterministic for the same inputs")
	}
}

func TestTokenChangesWithWindowKeyAndDevice(t *testing.T) {
	base := Token(key, Window(now), "laptop")
	others := map[string]string{
		"next window": Token(key, Window(now)+1, "laptop"),
		"other key":   Token([]byte("another-discovery-key-32-bytes!!"), Window(now), "laptop"),
		"other dev":   Token(key, Window(now), "iphone"),
	}
	for name, tok := range others {
		if tok == base {
			t.Fatalf("%s produced the same token", name)
		}
	}
}

func TestMatchToleratesOneWindowSkew(t *testing.T) {
	members := []string{"iphone", "laptop"}
	w := Window(now)
	if id, ok := Match(key, members, Token(key, w-1, "laptop"), now); !ok || id != "laptop" {
		t.Fatalf("token from previous window should match laptop, got %q %v", id, ok)
	}
	if _, ok := Match(key, members, Token(key, w-2, "laptop"), now); ok {
		t.Fatal("token from two windows ago must not match")
	}
}

func TestMatchRejectsOutsider(t *testing.T) {
	outsider := Token([]byte("outsider-key-outsider-key-32byte"), Window(now), "laptop")
	if _, ok := Match(key, []string{"laptop"}, outsider, now); ok {
		t.Fatal("token made with another key must not match")
	}
}
