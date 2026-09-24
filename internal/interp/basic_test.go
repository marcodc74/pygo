package interp

import "testing"

func TestHello(t *testing.T) {
	expectOut(t, `
fn main() {
    let name = "mondo"
    print("ciao ${name}", 1 + 2, 1.5, [1, "a"], {"k": true}, nil)
}
`, `ciao mondo 3 1.5 [1, "a"] {"k": true} nil`)
}
