package auth

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
)

const testPassword = "correct horse battery staple"

var phcV1 = regexp.MustCompile(`^\$argon2id\$v=19\$m=19456,t=2,p=1\$[A-Za-z0-9+/]{22}\$[A-Za-z0-9+/]{43}$`)

// Task 8.1: round-trip under parameter set v1.
func TestHashRoundTrip(t *testing.T) {
	h := NewHasher(ParamsV1)
	ctx := context.Background()
	a, err := h.Hash(ctx, []byte(testPassword))
	if err != nil {
		t.Fatal(err)
	}
	if !phcV1.MatchString(a) {
		t.Fatalf("hash %q is not a v1 argon2id PHC string with a 16-byte salt and 32-byte key", a)
	}
	if strings.Contains(a, testPassword) {
		t.Fatal("hash contains the password")
	}
	b, err := h.Hash(ctx, []byte(testPassword))
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("two hashes of one password are identical; the salt is not random")
	}
	match, outdated, err := h.Verify(ctx, a, []byte(testPassword))
	if err != nil || !match || outdated {
		t.Fatalf("Verify = %v, %v, %v; want match, current", match, outdated, err)
	}
}

// Task 8.1: wrong password.
func TestVerifyWrongPassword(t *testing.T) {
	h := NewHasher(ParamsV1)
	ctx := context.Background()
	enc, err := h.Hash(ctx, []byte(testPassword))
	if err != nil {
		t.Fatal(err)
	}
	match, _, err := h.Verify(ctx, enc, []byte(testPassword+"!"))
	if err != nil || match {
		t.Fatalf("Verify(wrong) = %v, %v; want no match", match, err)
	}
}

// Task 8.1: a hash under other parameters still verifies and is flagged for rehash.
func TestVerifyFlagsOutdatedParameters(t *testing.T) {
	ctx := context.Background()
	old := NewHasher(Params{Memory: 8192, Time: 1, Threads: 1})
	enc, err := old.Hash(ctx, []byte(testPassword))
	if err != nil {
		t.Fatal(err)
	}
	match, outdated, err := NewHasher(ParamsV1).Verify(ctx, enc, []byte(testPassword))
	if err != nil || !match || !outdated {
		t.Fatalf("Verify = %v, %v, %v; want match, outdated", match, outdated, err)
	}
}

func TestVerifyRejectsMalformedHash(t *testing.T) {
	h := NewHasher(Params{Memory: 64, Time: 1, Threads: 1})
	good, err := h.Hash(context.Background(), []byte(testPassword))
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(good, "$")
	for name, enc := range map[string]string{
		"empty":         "",
		"argon2i":       strings.Replace(good, "$argon2id$", "$argon2i$", 1),
		"version 16":    strings.Replace(good, "$v=19$", "$v=16$", 1),
		"missing param": "$argon2id$v=19$m=64,t=1$" + parts[4] + "$" + parts[5],
		"zero memory":   "$argon2id$v=19$m=0,t=1,p=1$" + parts[4] + "$" + parts[5],
		"extra param":   "$argon2id$v=19$m=64,t=1,p=1,x=1$" + parts[4] + "$" + parts[5],
		"bad salt":      "$argon2id$v=19$m=64,t=1,p=1$!!!$" + parts[5],
		"trailing part": good + "$x",
	} {
		if _, _, err := h.Verify(context.Background(), enc, []byte(testPassword)); !errors.Is(err, ErrMalformedHash) {
			t.Errorf("%s: err = %v, want ErrMalformedHash", name, err)
		}
	}
}

// Task 8.1: at most two Argon2 computations run at once.
func TestVerifyWaitsForFreeSlot(t *testing.T) {
	h := NewHasher(Params{Memory: 64, Time: 1, Threads: 1})
	enc, err := h.Hash(context.Background(), []byte(testPassword))
	if err != nil {
		t.Fatal(err)
	}
	// Occupy both slots, as two in-flight verifications would.
	h.slots <- struct{}{}
	h.slots <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := h.Verify(ctx, enc, []byte(testPassword)); !errors.Is(err, context.Canceled) {
		t.Fatalf("Verify with both slots busy = %v, want it to wait (and give up on cancel)", err)
	}
	h.release()
	if match, _, err := h.Verify(context.Background(), enc, []byte(testPassword)); err != nil || !match {
		t.Fatalf("Verify after a slot freed = %v, %v", match, err)
	}
}
