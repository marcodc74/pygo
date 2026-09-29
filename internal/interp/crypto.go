package interp

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"strings"
)

// crypto implements the `crypto` and `jwt` stdlib modules. Hashing, HMAC,
// constant-time comparison, PBKDF2 and JWT signing/verification are pure
// (no capability); only randomness is an effect. The bytes come from the
// interpreter RNG, so a run is reproducible under --seed and every engine
// agrees.
func init() {
	register("crypto", map[string]BuiltinFn{
		"sha256": func(th *Thread, _ Value, a []Value) (Value, error) {
			sum := sha256.Sum256([]byte(a[0].(string)))
			return hex.EncodeToString(sum[:]), nil
		},
		"hmac_sha256": func(th *Thread, _ Value, a []Value) (Value, error) {
			return hex.EncodeToString(hmacSHA256(a[0].(string), a[1].(string))), nil
		},
		"equal": func(th *Thread, _ Value, a []Value) (Value, error) {
			return ctEqual(a[0].(string), a[1].(string)), nil
		},
		"pbkdf2": func(th *Thread, _ Value, a []Value) (Value, error) {
			iters, length := a[2].(int64), a[3].(int64)
			if iters < 1 || iters > 10_000_000 {
				return nil, th.fail("E_CRYPTO", "pbkdf2: iterations must be between 1 and 10000000, got %d", iters)
			}
			if length < 0 || length > 1<<20 {
				return nil, th.fail("E_CRYPTO", "pbkdf2: length must be between 0 and 1048576, got %d", length)
			}
			dk := pbkdf2SHA256([]byte(a[0].(string)), []byte(a[1].(string)), int(iters), int(length))
			return hex.EncodeToString(dk), nil
		},
		"random_bytes": func(th *Thread, _ Value, a []Value) (Value, error) {
			n := a[0].(int64)
			if n < 0 || n > 1<<20 {
				return nil, th.fail("E_CRYPTO", "random_bytes: n must be between 0 and 1048576, got %d", n)
			}
			b := make([]byte, n)
			th.in.rngMu.Lock()
			for i := range b {
				b[i] = byte(th.in.rng.Intn(256))
			}
			th.in.rngMu.Unlock()
			return hex.EncodeToString(b), nil
		},
	})

	register("jwt", map[string]BuiltinFn{
		"sign_hs256": func(th *Thread, _ Value, a []Value) (Value, error) {
			var payload bytes.Buffer
			if err := encodeJSON(&payload, a[0], 0, 0); err != nil {
				return nil, th.fail("E_JWT", "sign_hs256: %v", err)
			}
			header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
			body := base64.RawURLEncoding.EncodeToString(payload.Bytes())
			sig := base64.RawURLEncoding.EncodeToString(hmacSHA256(a[1].(string), header+"."+body))
			return header + "." + body + "." + sig, nil
		},
		"verify_hs256": func(th *Thread, _ Value, a []Value) (Value, error) {
			token, key := a[0].(string), a[1].(string)
			parts := strings.Split(token, ".")
			if len(parts) != 3 {
				return nil, th.fail("E_JWT", "verify_hs256: token must have 3 dot-separated parts")
			}
			sig, err := base64.RawURLEncoding.DecodeString(parts[2])
			if err != nil {
				return nil, th.fail("E_JWT", "verify_hs256: bad signature encoding")
			}
			if !hmac.Equal(sig, hmacSHA256(key, parts[0]+"."+parts[1])) {
				return nil, th.fail("E_JWT", "verify_hs256: signature mismatch")
			}
			headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
			if err != nil {
				return nil, th.fail("E_JWT", "verify_hs256: bad header encoding")
			}
			header, err := decodeJSON(string(headerJSON))
			if err != nil {
				return nil, th.fail("E_JWT", "verify_hs256: bad header JSON")
			}
			hm, ok := header.(*Map)
			if !ok {
				return nil, th.fail("E_JWT", "verify_hs256: header is not an object")
			}
			if alg, _ := hm.Get("alg"); alg != "HS256" {
				return nil, th.fail("E_JWT", "verify_hs256: unsupported alg %s", Repr(alg))
			}
			payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
			if err != nil {
				return nil, th.fail("E_JWT", "verify_hs256: bad payload encoding")
			}
			claims, err := decodeJSON(string(payloadJSON))
			if err != nil {
				return nil, th.fail("E_JWT", "verify_hs256: bad payload JSON")
			}
			cm, ok := claims.(*Map)
			if !ok {
				return nil, th.fail("E_JWT", "verify_hs256: payload is not an object")
			}
			return cm, nil
		},
	})
}

func hmacSHA256(key, data string) []byte {
	m := hmac.New(sha256.New, []byte(key))
	m.Write([]byte(data))
	return m.Sum(nil)
}

// ctEqual compares a and b without an early exit on the first differing byte.
func ctEqual(a, b string) bool {
	ab, bb := []byte(a), []byte(b)
	if len(ab) != len(bb) {
		subtle.ConstantTimeCompare(ab, ab) // burn a compare of matching length
		return false
	}
	return subtle.ConstantTimeCompare(ab, bb) == 1
}

// pbkdf2SHA256 is PBKDF2 with HMAC-SHA256 as the PRF (RFC 8018, §5.2).
func pbkdf2SHA256(password, salt []byte, iter, keyLen int) []byte {
	prf := hmac.New(sha256.New, password)
	hashLen := prf.Size()
	blocks := (keyLen + hashLen - 1) / hashLen
	dk := make([]byte, 0, blocks*hashLen)
	u := make([]byte, hashLen)
	t := make([]byte, hashLen)
	var counter [4]byte
	for block := 1; block <= blocks; block++ {
		prf.Reset()
		prf.Write(salt)
		binary.BigEndian.PutUint32(counter[:], uint32(block))
		prf.Write(counter[:])
		u = prf.Sum(u[:0])
		copy(t, u)
		for n := 2; n <= iter; n++ {
			prf.Reset()
			prf.Write(u)
			u = prf.Sum(u[:0])
			for i := range t {
				t[i] ^= u[i]
			}
		}
		dk = append(dk, t...)
	}
	return dk[:keyLen]
}
