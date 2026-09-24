package sig

import "testing"

func TestHeadersParse(t *testing.T) {
	ms := Modules()
	for _, n := range []string{"core", "json", "fs", "os", "http", "time", "log", "math", "re", "proc", "rand"} {
		if ms[n] == nil {
			t.Fatalf("missing module %s", n)
		}
	}
	if ms["core"].Methods["List"]["map"] == nil {
		t.Fatal("List.map missing")
	}
	if !ms["fs"].Funcs["read"].Fallible || ms["fs"].Funcs["read"].Uses[0] != "fs" {
		t.Fatal("fs.read signature wrong")
	}
	if !ms["core"].Funcs["print"].Params[0].Variadic {
		t.Fatal("print not variadic")
	}
}
