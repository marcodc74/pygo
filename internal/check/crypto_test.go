package check

import "testing"

// Hashing, HMAC and JWT are pure; only random_bytes carries the crypto effect.
func TestCryptoTypes(t *testing.T) {
	expectCodes(t, `
import "crypto"
import "jwt"

fn nonce(n: Int) -> !Str uses crypto {
    return try crypto.random_bytes(n)
}

fn digest(data: Str) -> Str => crypto.sha256(data)

fn claims(token: Str, key: Str) -> !Map[Str, Any] {
    return try jwt.verify_hs256(token, key: key)
}
`)

	// random_bytes without the declaration is an undeclared effect
	expectCodes(t, `
import "crypto"

fn nonce() -> !Str {
    return try crypto.random_bytes(16)
}
`, "E0501")
}

func TestCryptoArgTypes(t *testing.T) {
	// pbkdf2 wants Int for iterations, and signing wants a Map, not a List
	expectCodes(t, `
import "crypto"
import "jwt"

fn main() -> ! {
    print(try crypto.pbkdf2("p", salt: "s", iterations: "many", length: 32))
    print(try jwt.sign_hs256(["sub"], key: "k"))
}
`, "E0301", "E0301")
}
