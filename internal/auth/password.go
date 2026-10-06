// Package auth implements the single-administrator account (admin-auth spec,
// design D12): Argon2id password hashing, the credential row, server-side
// sessions, login throttling, and the security audit trail.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Params are Argon2id work parameters.
type Params struct {
	// Memory is the memory cost in KiB.
	Memory uint32
	// Time is the number of passes.
	Time uint32
	// Threads is the degree of parallelism.
	Threads uint8
}

// ParamsV1 is parameter set v1 (the OWASP baseline, design D12).
var ParamsV1 = Params{Memory: 19456, Time: 2, Threads: 1}

const (
	saltLen = 16
	keyLen  = 32
	// verifySlots caps concurrent Argon2 computations to bound memory use.
	verifySlots = 2
)

// ErrMalformedHash is returned for a stored hash that is not an Argon2id PHC string.
var ErrMalformedHash = errors.New("auth: stored password hash is not a valid argon2id PHC string")

// PasswordHasher hashes and verifies passwords. Hasher is the production
// implementation; the interface lets tests observe verification calls.
type PasswordHasher interface {
	// Hash returns a PHC string for password under the current parameters.
	Hash(ctx context.Context, password []byte) (string, error)
	// Verify reports whether password matches encoded and whether encoded
	// uses parameters other than the current ones (and should be rehashed).
	Verify(ctx context.Context, encoded string, password []byte) (match, outdated bool, err error)
}

// Hasher computes Argon2id hashes with at most two computations in flight.
type Hasher struct {
	params Params
	slots  chan struct{}
}

// NewHasher returns a Hasher whose current parameters are p.
func NewHasher(p Params) *Hasher {
	return &Hasher{params: p, slots: make(chan struct{}, verifySlots)}
}

func (h *Hasher) acquire(ctx context.Context) error {
	select {
	case h.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *Hasher) release() { <-h.slots }

// Hash implements PasswordHasher.
func (h *Hasher) Hash(ctx context.Context, password []byte) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: read salt: %w", err)
	}
	if err := h.acquire(ctx); err != nil {
		return "", err
	}
	defer h.release()
	key := argon2.IDKey(password, salt, h.params.Time, h.params.Memory, h.params.Threads, keyLen)
	return encodePHC(h.params, salt, key), nil
}

// Verify implements PasswordHasher. The comparison is constant-time.
func (h *Hasher) Verify(ctx context.Context, encoded string, password []byte) (bool, bool, error) {
	p, salt, want, err := decodePHC(encoded)
	if err != nil {
		return false, false, err
	}
	if err := h.acquire(ctx); err != nil {
		return false, false, err
	}
	defer h.release()
	got := argon2.IDKey(password, salt, p.Time, p.Memory, p.Threads, uint32(len(want)))
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return false, false, nil
	}
	outdated := p != h.params || len(salt) != saltLen || len(want) != keyLen
	return true, outdated, nil
}

var b64 = base64.RawStdEncoding

// encodePHC renders $argon2id$v=19$m=…,t=…,p=…$salt$hash.
func encodePHC(p Params, salt, key []byte) string {
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.Memory, p.Time, p.Threads, b64.EncodeToString(salt), b64.EncodeToString(key))
}

func decodePHC(s string) (Params, []byte, []byte, error) {
	// "", "argon2id", "v=19", "m=…,t=…,p=…", salt, hash
	parts := strings.Split(s, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return Params{}, nil, nil, ErrMalformedHash
	}
	if parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return Params{}, nil, nil, fmt.Errorf("%w: unsupported version %q", ErrMalformedHash, parts[2])
	}
	var p Params
	seen := map[string]bool{}
	for _, kv := range strings.Split(parts[3], ",") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || seen[k] {
			return Params{}, nil, nil, ErrMalformedHash
		}
		seen[k] = true
		switch k {
		case "m":
			n, err := strconv.ParseUint(v, 10, 32)
			if err != nil || n == 0 {
				return Params{}, nil, nil, ErrMalformedHash
			}
			p.Memory = uint32(n)
		case "t":
			n, err := strconv.ParseUint(v, 10, 32)
			if err != nil || n == 0 {
				return Params{}, nil, nil, ErrMalformedHash
			}
			p.Time = uint32(n)
		case "p":
			n, err := strconv.ParseUint(v, 10, 8)
			if err != nil || n == 0 {
				return Params{}, nil, nil, ErrMalformedHash
			}
			p.Threads = uint8(n)
		default:
			return Params{}, nil, nil, ErrMalformedHash
		}
	}
	if len(seen) != 3 {
		return Params{}, nil, nil, ErrMalformedHash
	}
	salt, err := b64.DecodeString(parts[4])
	if err != nil || len(salt) < 8 {
		return Params{}, nil, nil, ErrMalformedHash
	}
	key, err := b64.DecodeString(parts[5])
	if err != nil || len(key) < 16 {
		return Params{}, nil, nil, ErrMalformedHash
	}
	return p, salt, key, nil
}
