package interp

import (
	"bufio"
	"crypto/md5"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// This file is the adversarial pass over the `sql` module: a hostile or broken
// server must always produce a clean E_SQL failure (exit 1), never a Go panic
// (R0099, exit 2), and handles must survive misuse.

// startRawPG runs handler for every connection; handler owns the protocol.
func startRawPG(t *testing.T, handler func(net.Conn)) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer nc.Close()
				handler(nc)
			}()
		}
	}()
	return ln.Addr().String(), func() { ln.Close() }
}

// readStartup consumes the startup packet (answering an SSLRequest with 'N').
func readStartup(nc net.Conn) bool {
	r := bufio.NewReader(nc)
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return false
	}
	body := make([]byte, binary.BigEndian.Uint32(hdr)-4)
	if _, err := io.ReadFull(r, body); err != nil {
		return false
	}
	if binary.BigEndian.Uint32(body) == 80877103 {
		nc.Write([]byte{'N'})
		if _, err := io.ReadFull(r, hdr); err != nil {
			return false
		}
		body = make([]byte, binary.BigEndian.Uint32(hdr)-4)
		if _, err := io.ReadFull(r, body); err != nil {
			return false
		}
	}
	return true
}

// mockStartupOK authenticates with trust and reports ReadyForQuery.
func mockStartupOK(nc net.Conn) bool {
	if !readStartup(nc) {
		return false
	}
	mockWrite(nc, 'R', int32b(0))
	mockWrite(nc, 'S', append(cstr("server_version"), cstr("16.0")...))
	mockWrite(nc, 'K', append(int32b(1), int32b(2)...))
	mockWrite(nc, 'Z', []byte{'I'})
	return true
}

func TestSQLHostileServer(t *testing.T) {
	cases := []struct {
		name    string
		handler func(net.Conn)
	}{
		{"abrupt close", func(nc net.Conn) {}},
		{"garbage bytes", func(nc net.Conn) {
			nc.Write([]byte("definitely not the postgres protocol at all, not even close"))
		}},
		{"bad message length", func(nc net.Conn) {
			if !mockStartupOK(nc) {
				return
			}
			nc.Write([]byte{'T', 0, 0, 0, 2}) // a length smaller than its own header
		}},
		{"unsupported auth", func(nc net.Conn) {
			if !readStartup(nc) {
				return
			}
			mockWrite(nc, 'R', int32b(7)) // AuthenticationGSS: unsupported
		}},
		{"hanging server", func(nc net.Conn) {
			mockStartupOK(nc)
			time.Sleep(2 * time.Second)
		}},
		{"silent server", func(nc net.Conn) {
			time.Sleep(2 * time.Second) // accepts, never answers, never closes
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			addr, closeFn := startRawPG(t, c.handler)
			defer closeFn()
			src := `
import "sql"

fn code(dsn: Str) -> Str uses sql {
    let c = try sql.open(dsn, timeout_ms: 300)
    let _ = sql.query(c, text: sql"SELECT 1", timeout_ms: 300) catch e { return e.code }
    return "ok"
}

fn main() -> ! uses sql {
    print(code("postgres://u@` + addr + `/db?sslmode=disable"))
}
`
			out, res := runSrc(t, src, "sql")
			if res.Status != "ok" {
				t.Fatalf("status %s: %s\ntrace: %v", res.Status, res.Describe(), out)
			}
			if out != "E_SQL\n" {
				t.Fatalf("got %q, want a clean E_SQL failure", out)
			}
		})
	}
}

func TestSQLTLSRejected(t *testing.T) {
	addr, closeFn := startRawPG(t, func(nc net.Conn) {
		buf := make([]byte, 8)
		if _, err := io.ReadFull(nc, buf); err != nil {
			return
		}
		nc.Write([]byte{'X'}) // not 'S' and not 'N'
	})
	defer closeFn()
	src := `
import "sql"

fn code(dsn: Str) -> Str uses sql {
    let c = try sql.open(dsn, timeout_ms: 500)
    let _ = sql.query(c, text: sql"SELECT 1") catch e { return e.code }
    return "ok"
}

fn main() -> ! uses sql {
    print(code("postgres://u@` + addr + `/db?sslmode=require"))
}
`
	out, res := runSrc(t, src, "sql")
	if res.Status != "ok" || out != "E_SQL\n" {
		t.Fatalf("status %s: got %q (%s)", res.Status, out, res.Describe())
	}
}

// Handles are opaque: closing is idempotent, a closed or fabricated handle is
// a clean E_SQL failure, and a handle of the wrong type is a programmer panic.
func TestSQLHandleMisuse(t *testing.T) {
	srv := startMockPG(t)
	defer srv.Close()
	src := `
import "sql"

fn probe(c: sql.Conn) -> Str uses sql {
    let _ = sql.query(c, text: sql"SELECT 1") catch e { return e.code }
    return "ok"
}

fn main() -> ! uses sql {
    let c = try sql.open("postgres://u@` + srv.Addr() + `/db?sslmode=disable")
    sql.close(c)
    sql.close(c)
    print("closed twice")
    print(probe(c))
    print("fabricated", probe(sql.Conn{id: 999}))
}
`
	out, res := runSrc(t, src, "sql")
	if res.Status != "ok" {
		t.Fatalf("status %s: %s\n%s", res.Status, res.Describe(), out)
	}
	want := "closed twice\nE_SQL\nfabricated E_SQL\n"
	if out != want {
		t.Fatalf("got %q, want %q", out, want)
	}
}

func TestSQLArgumentTypes(t *testing.T) {
	srv := startMockPG(t)
	defer srv.Close()
	src := `
import "sql"

fn bad(c: sql.Conn) -> Str uses sql {
    let _ = sql.exec(c, text: sql"SELECT $1", args: [[1, 2]]) catch e { return "${e.code} ${e.message}" }
    return "ok"
}

fn main() -> ! uses sql {
    let c = try sql.open("postgres://u@` + srv.Addr() + `/db?sslmode=disable")
    print(bad(c))
    let rows = try sql.query(c, text: sql"SELECT $1, $2, $3", args: [7, "hi", true])
    print(repr(rows[0]["i"]), repr(rows[0]["s"]), repr(rows[0]["b"]))
    let nulls = try sql.query(c, text: sql"SELECT $1, $2", args: [nil, 1])
    print(repr(nulls[0]["a"]))
    sql.close(c)
}
`
	out, res := runSrc(t, src, "sql")
	if res.Status != "ok" {
		t.Fatalf("status %s: %s\n%s", res.Status, res.Describe(), out)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "E_SQL unsupported parameter type") {
		t.Fatalf("line 1: %q", lines[0])
	}
	if lines[1] != `7 "hi" true` {
		t.Fatalf("typed echo: %q", lines[1])
	}
	if lines[2] != "nil" {
		t.Fatalf("null: %q", lines[2])
	}
}

func TestSQLSchemaErrors(t *testing.T) {
	srv := startMockPG(t)
	defer srv.Close()
	src := `
import "sql"

struct Wrong { id: Str, name: Str }
struct Missing { id: Int, nope: Str }

fn wrong(c: sql.Conn) -> Str uses sql {
    let _ = sql.query_as(c, text: sql"SELECT items", schema: Wrong) catch e { return e.code }
    return "ok"
}

fn missing(c: sql.Conn) -> Str uses sql {
    let _ = sql.query_as(c, text: sql"SELECT items", schema: Missing) catch e { return e.code }
    return "ok"
}

fn main() -> ! uses sql {
    let c = try sql.open("postgres://u@` + srv.Addr() + `/db?sslmode=disable")
    print(wrong(c))
    print(missing(c))
    sql.close(c)
}
`
	out, res := runSrc(t, src, "sql")
	if res.Status != "ok" || out != "E_SCHEMA\nE_SCHEMA\n" {
		t.Fatalf("status %s: got %q (%s)", res.Status, out, res.Describe())
	}
}

func TestSQLScannerEdgeCases(t *testing.T) {
	cases := []struct {
		q    string
		args int
		want string
		err  bool
	}{
		{q: "SELECT /* a /* nested ? */ b */ ?", args: 1, want: "SELECT /* a /* nested ? */ b */ $1"},
		{q: "SELECT ? /* unterminated", args: 1, want: "SELECT $1 /* unterminated"},
		{q: `SELECT "a?b" FROM t`, args: 0, want: `SELECT "a?b" FROM t`},
		{q: "SELECT '$1', ?", args: 1, want: "SELECT '$1', $1"},
		{q: "SELECT $x$?$x$, ?", args: 1, want: "SELECT $x$?$x$, $1"},
		{q: "-- ?\nSELECT ?", args: 1, want: "-- ?\nSELECT $1"},
		{q: "SELECT ? -- trailing ?", args: 1, want: "SELECT $1 -- trailing ?"},
	}
	for _, c := range cases {
		got, msg := prepareSQL(SqlStr(c.q), make([]Value, c.args))
		if c.err {
			if msg == "" {
				t.Errorf("prepareSQL(%q): expected an error", c.q)
			}
			continue
		}
		if msg != "" {
			t.Errorf("prepareSQL(%q): %s", c.q, msg)
			continue
		}
		if got != c.want {
			t.Errorf("prepareSQL(%q) = %q, want %q", c.q, got, c.want)
		}
	}
}

func TestSQLDecodeEdgeCases(t *testing.T) {
	if v := decodeSQL([]byte("9223372036854775808"), 20); v != "9223372036854775808" {
		t.Errorf("int8 overflow should stay a Str: %v", v)
	}
	if v := decodeSQL([]byte("1e3"), 1700); v != 1000.0 {
		t.Errorf("numeric exponent: %v", v)
	}
	if v := decodeSQL([]byte(`\xdeadbeef`), 17); v != `\xdeadbeef` {
		t.Errorf("bytea: %v", v)
	}
	if v := decodeSQL([]byte(""), 25); v != "" {
		t.Errorf("empty text: %v", v)
	}
	if v := decodeSQL([]byte("caffè ☕"), 25); v != "caffè ☕" {
		t.Errorf("unicode: %v", v)
	}
	if v := decodeSQL([]byte("f"), 16); v != false {
		t.Errorf("false: %v", v)
	}
}

// Malformed backend messages must be rejected, never panic.
func TestSQLMalformedMessages(t *testing.T) {
	for _, b := range [][]byte{nil, {}, {0}, {0, 1}, {0, 2, 1}} {
		if _, err := parseRowDescription(b); err == nil {
			t.Errorf("parseRowDescription(%v) accepted a malformed payload", b)
		}
		if _, err := parseDataRow(b); err == nil {
			t.Errorf("parseDataRow(%v) accepted a malformed payload", b)
		}
	}
	if err := pgError(nil); err == nil {
		t.Error("pgError(nil) should fail")
	}
	if err := pgError([]byte{'M', 'x'}); err == nil {
		t.Error("pgError with an unterminated field should fail")
	}
}

func TestSQLDSNMore(t *testing.T) {
	cases := []struct {
		in                         string
		host, port, user, password string
		db, ssl                    string
		err                        bool
	}{
		{in: "postgres://u:p%40ss@[::1]:5433/db", host: "::1", port: "5433", user: "u", password: "p@ss", db: "db", ssl: "prefer"},
		{in: "host=/var/run/postgresql user=ada dbname=db", host: "/var/run/postgresql", port: "5432", user: "ada", db: "db", ssl: "prefer"},
		{in: "dbname=db sslmode=disable", host: "localhost", port: "5432", db: "db", ssl: "disable"},
		{in: "sslmode=DISABLE", err: true},
		{in: "sslmode=require extra=", err: false, ssl: "require", host: "localhost", port: "5432"},
		{in: "postgres://", err: false, host: "localhost", port: "5432", ssl: "prefer"},
	}
	for _, c := range cases {
		d, err := parseDSN(c.in)
		if c.err {
			if err == nil {
				t.Errorf("parseDSN(%q): expected an error", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseDSN(%q): %v", c.in, err)
			continue
		}
		if d.host != c.host || d.port != c.port || d.user != c.user || d.password != c.password || d.dbname != c.db || d.sslmode != c.ssl {
			t.Errorf("parseDSN(%q) = %+v", c.in, d)
		}
	}
}

// The MD5 password is md5(md5(password+user)+salt), prefixed with "md5":
// make sure our implementation matches an independent computation.
func TestSQLMD5Independent(t *testing.T) {
	user, pw := "ada", "hunter2"
	salt := []byte{9, 8, 7, 6}
	inner := md5.Sum([]byte(pw + user))
	outer := md5.Sum(append([]byte(hex.EncodeToString(inner[:])), salt...))
	want := "md5" + hex.EncodeToString(outer[:])
	if got := md5Password(user, pw, salt); got != want {
		t.Fatalf("md5Password = %q, want %q", got, want)
	}
}

func TestSQLScramFailures(t *testing.T) {
	if _, err := newScramClient([]byte("SCRAM-SHA-1\x00"), "u", "p"); err == nil {
		t.Error("accepted a server that does not offer SCRAM-SHA-256")
	}
	client, err := newScramClient([]byte("SCRAM-SHA-256\x00"), "u", "p")
	if err != nil {
		t.Fatal(err)
	}
	nonce := strings.TrimPrefix(client.clientFirst(), "n,,n=,r=")
	salt := base64.StdEncoding.EncodeToString([]byte("salt"))
	if _, err := client.finish("r=WRONG,s=" + salt + ",i=4096"); err == nil {
		t.Error("accepted a server nonce that does not extend the client nonce")
	}
	if _, err := client.finish("r=" + nonce + "x,s=###,i=4096"); err == nil {
		t.Error("accepted an invalid salt")
	}
	if _, err := client.finish("r=" + nonce + "x,s=" + salt + ",i=zero"); err == nil {
		t.Error("accepted an invalid iteration count")
	}
}

func TestSQLScannerNoPanic(t *testing.T) {
	inputs := []string{"", "?", "$", "$$", "$1$", "'", "\"", "/*", "*/", "--", "'?\"$/*--", "$tag$", "?$?$$?/*?*/?--?\n?", "$1 ? $2"}
	for _, in := range inputs {
		_, _ = scanSQL(in)
		_, _ = prepareSQL(SqlStr(in), []Value{int64(1)})
		_, _ = prepareSQL(SqlStr(in), nil)
	}
}

func TestSQLConcurrentUse(t *testing.T) {
	srv := startMockPG(t)
	defer srv.Close()
	src := `
import "sql"

fn one(c: sql.Conn) -> !Int uses sql {
    let rows = try sql.query(c, text: sql"SELECT one")
    return int(rows[0]["n"])
}

fn main() -> ! uses sql {
    let c = try sql.open("postgres://u@` + srv.Addr() + `/db?sslmode=disable")
    let a = spawn one(c)
    let b = spawn one(c)
    print(try a.wait(), try b.wait())
    sql.close(c)
}
`
	out, res := runSrc(t, src, "sql")
	if res.Status != "ok" || out != "1 1\n" {
		t.Fatalf("status %s: got %q (%s)", res.Status, out, res.Describe())
	}
}

func TestSQLTimeoutValues(t *testing.T) {
	if _, err := msDuration(int64(-1)); err == nil {
		t.Fatal("negative timeout accepted")
	}
	if d, err := msDuration(int64(0)); err != nil || d != 0 {
		t.Fatalf("zero timeout: %v %v", d, err)
	}
	if d, err := msDuration(int64(1500)); err != nil || d != 1500*time.Millisecond {
		t.Fatalf("1500ms: %v %v", d, err)
	}
}
