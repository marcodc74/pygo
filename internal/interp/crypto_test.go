package interp

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestCryptoVectors(t *testing.T) {
	expectOut(t, `
import "crypto"

fn main() -> ! {
    print(crypto.sha256("abc"))
    print(crypto.sha256(""))
    print(crypto.hmac_sha256("key", data: "The quick brown fox jumps over the lazy dog"))
    print(try crypto.pbkdf2("password", salt: "salt", iterations: 1, length: 32))
    print(try crypto.pbkdf2("password", salt: "salt", iterations: 4096, length: 32))
    print(crypto.equal("token", b: "token"), crypto.equal("token", b: "tokeN"), crypto.equal("a", b: "ab"))
}
`, `ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad
e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
f7bc83f430538424b13298e6aa6fb143ef4d59a14946175997479dbc2d1a3cd8
120fb6cffcf8b32c43e7225256c4f837a86548c92ccc35480805987cb70be17b
c5e478d59288c841aa530db6845c4c8d962893a001ce4e11a4963873aa98134a
true false false`)
}

func TestJWT(t *testing.T) {
	expectOut(t, `
import "jwt"

fn main() -> ! {
    let token = try jwt.sign_hs256({"sub": "ada", "n": 2, "admin": true}, key: "s3cret")
    let claims = try jwt.verify_hs256(token, key: "s3cret")
    print(claims["sub"], claims["n"], claims["admin"])
    let wrong = try jwt.verify_hs256(token, key: "wrong") catch e { e.code }
    print(wrong)
    let bits = token.split(".")
    let forged = "${bits[0]}.${bits[1]}.AAAA"
    let tampered = try jwt.verify_hs256(forged, key: "s3cret") catch e { e.code }
    print(tampered)
    let junk = try jwt.verify_hs256("not.a.jwt", key: "k") catch e { e.code }
    print(junk)
}
`, `ada 2 true
E_JWT
E_JWT
E_JWT`)
}

func TestCryptoRandomDeterministic(t *testing.T) {
	src := `
import "crypto"
fn main() -> ! uses crypto {
    print(try crypto.random_bytes(8))
    print(try crypto.random_bytes(1))
}
`
	out, res := runSrc(t, src, "crypto")
	if res.Status != "ok" {
		t.Fatalf("status %s: %s\noutput: %s", res.Status, res.Describe(), out)
	}
	if lines := strings.Fields(out); len(lines) != 2 || len(lines[0]) != 16 || len(lines[1]) != 2 {
		t.Fatalf("random_bytes output is not hex of the requested length: %q", out)
	}
	out2, _ := runSrc(t, src, "crypto")
	if out != out2 {
		t.Fatalf("random_bytes is not reproducible under the seed: %q vs %q", out, out2)
	}
}

func TestCryptoPermissionDenied(t *testing.T) {
	_, res := runSrc(t, `
import "crypto"
fn main() -> ! uses crypto { print(try crypto.random_bytes(4)) }
`)
	if res.ExitCode != ExitPermission || !strings.Contains(res.Panic.Hint, "--allow crypto") {
		t.Fatalf("got %+v", res.Panic)
	}
}

// TestCryptoBounds checks the range guards on PBKDF2 and random_bytes.
func TestCryptoBounds(t *testing.T) {
	out, res := runSrc(t, `
import "crypto"

fn main() -> ! uses crypto {
    print(try crypto.pbkdf2("p", salt: "s", iterations: 0, length: 32) catch e { e.code })
    print(try crypto.pbkdf2("p", salt: "s", iterations: 10000001, length: 32) catch e { e.code })
    print(try crypto.pbkdf2("p", salt: "s", iterations: 1, length: -1) catch e { e.code })
    print(try crypto.pbkdf2("p", salt: "s", iterations: 1, length: 1048577) catch e { e.code })
    print(try crypto.random_bytes(-1) catch e { e.code })
    print(try crypto.random_bytes(1048577) catch e { e.code })
    print((try crypto.random_bytes(0)).len())
}
`, "crypto")
	if res.Status != "ok" {
		t.Fatalf("status %s: %s\noutput: %s", res.Status, res.Describe(), out)
	}
	want := strings.TrimSpace(strings.Repeat("E_CRYPTO\n", 6) + "0")
	if strings.TrimSpace(out) != want {
		t.Fatalf("output mismatch\n--- got ---\n%s\n--- want ---\n%s", out, want)
	}
}

// TestCryptoEqualLengths: different lengths and non-ASCII are handled without
// an early exit or a panic.
func TestCryptoEqualLengths(t *testing.T) {
	expectOut(t, `
import "crypto"

fn main() {
    print(crypto.equal("a", b: ""), crypto.equal("", b: "a"))
    print(crypto.equal("caffè", b: "caffè"), crypto.equal("caffè", b: "caffe"))
}
`, `false false
true false`)
}

// TestJWTAdversarial crafts tokens with Go so the edge cases where the
// signature is valid but the header/payload are wrong still get exercised.
func TestJWTAdversarial(t *testing.T) {
	b64 := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	sign := func(header, payload string) string {
		h, p := b64(header), b64(payload)
		return h + "." + p + "." + b64(string(hmacSHA256("k", h+"."+p)))
	}
	// every token must be rejected: `bad` returns true when verify fails
	tokens := []string{
		b64(`{"alg":"none","typ":"JWT"}`) + "." + b64(`{"sub":"ada"}`) + ".", // alg none, no signature
		sign(`{"alg":"HS256"}`, `[1,2,3]`),                                   // valid sig, payload is an array
		sign(`[]`, `{}`),                                                     // valid sig, header is an array
		sign(`{"alg":"HS256"}`, `{`),                                         // valid sig, malformed payload
		sign(`{"alg":"RS256"}`, `{"sub":"ada"}`),                             // valid sig, wrong alg
		"a.b.c.d",                                                            // four parts
		"a.b.***",                                                            // signature not base64
		"a.b",                                                                // two parts
		"",                                                                   // empty
	}
	var b strings.Builder
	b.WriteString("import \"jwt\"\nfn bad(t: Str) -> Bool { return (jwt.verify_hs256(t, key: \"k\") catch e { {} }).is_empty() }\nfn main() {\n")
	for _, tok := range tokens {
		b.WriteString("\tprint(bad(\"" + tok + "\"))\n")
	}
	b.WriteString("}\n")
	want := strings.TrimSpace(strings.Repeat("true\n", len(tokens)))
	expectOut(t, b.String(), want)
}
