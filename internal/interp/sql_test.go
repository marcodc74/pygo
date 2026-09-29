package interp

import (
	"bufio"
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/binary"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"
)

func TestSQLDSN(t *testing.T) {
	cases := []struct {
		in                string
		host, port, user  string
		password, db, ssl string
		wantErr           bool
	}{
		{in: "postgres://ada:pw@db.example:6543/shop?sslmode=require", host: "db.example", port: "6543", user: "ada", password: "pw", db: "shop", ssl: "require"},
		{in: "postgresql://ada@localhost/shop", host: "localhost", port: "5432", user: "ada", db: "shop", ssl: "prefer"},
		{in: "host=db port=6000 user=ada password='p w' dbname=shop sslmode=disable", host: "db", port: "6000", user: "ada", password: "p w", db: "shop", ssl: "disable"},
		{in: "", wantErr: true},
		{in: "not a dsn", wantErr: true},
		{in: "postgres://u@h/db?sslmode=bogus", wantErr: true},
	}
	for _, c := range cases {
		d, err := parseDSN(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseDSN(%q): expected error", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseDSN(%q): %v", c.in, err)
			continue
		}
		if d.host != c.host || d.port != c.port || d.user != c.user || d.password != c.password || d.dbname != c.db || d.sslmode != c.ssl {
			t.Errorf("parseDSN(%q) = %+v, want host=%s port=%s user=%s password=%s db=%s ssl=%s", c.in, d, c.host, c.port, c.user, c.password, c.db, c.ssl)
		}
	}
}

func TestSQLPlaceholders(t *testing.T) {
	cases := []struct {
		q    string
		args int
		want string
		err  bool
	}{
		{q: "SELECT $1, $2", args: 2, want: "SELECT $1, $2"},
		{q: "SELECT ? , ?", args: 2, want: "SELECT $1 , $2"},
		{q: "SELECT '?' , ?", args: 1, want: "SELECT '?' , $1"},
		{q: "SELECT $$ ? $$, ?", args: 1, want: "SELECT $$ ? $$, $1"},
		{q: "SELECT $tag$ ? $tag$, ?", args: 1, want: "SELECT $tag$ ? $tag$, $1"},
		{q: "data ? 'key'", args: 0, want: "data ? 'key'"},        // jsonb operator, no args
		{q: "data ? $1", args: 1, want: "data ? $1"},              // $ wins, left alone
		{q: "SELECT ? -- ?\n", args: 1, want: "SELECT $1 -- ?\n"}, // comment skipped
		{q: "SELECT /* ? */ ?", args: 1, want: "SELECT /* ? */ $1"},
		{q: "SELECT 1", args: 0, want: "SELECT 1"},
		{q: "SELECT 1", args: 1, err: true},
		{q: "SELECT ?, ?", args: 1, err: true},
	}
	for _, c := range cases {
		got, msg := prepareSQL(SqlStr(c.q), make([]Value, c.args))
		if c.err {
			if msg == "" {
				t.Errorf("prepareSQL(%q, %d): expected an error", c.q, c.args)
			}
			continue
		}
		if msg != "" {
			t.Errorf("prepareSQL(%q, %d): %s", c.q, c.args, msg)
			continue
		}
		if got != c.want {
			t.Errorf("prepareSQL(%q, %d) = %q, want %q", c.q, c.args, got, c.want)
		}
	}
}

func TestSQLDecode(t *testing.T) {
	if v := decodeSQL([]byte("42"), 23); v != int64(42) {
		t.Errorf("int4: %v", v)
	}
	if v := decodeSQL([]byte("3.5"), 701); v != 3.5 {
		t.Errorf("float8: %v", v)
	}
	if v := decodeSQL([]byte("t"), 16); v != true {
		t.Errorf("bool: %v", v)
	}
	if v := decodeSQL([]byte("hi"), 25); v != "hi" {
		t.Errorf("text: %v", v)
	}
	if v := decodeSQL(nil, 23); v != nil {
		t.Errorf("null: %v", v)
	}
	if v := decodeSQL([]byte("12.50"), 1700); v != 12.5 {
		t.Errorf("numeric float: %v", v)
	}
	if v := decodeSQL([]byte("7"), 1700); v != int64(7) {
		t.Errorf("numeric int: %v", v)
	}
	for tag, want := range map[string]int64{"INSERT 0 3": 3, "UPDATE 5": 5, "DELETE 2": 2, "SELECT 1": 1, "CREATE TABLE": 0, "": 0} {
		if got := commandRows(tag); got != want {
			t.Errorf("commandRows(%q) = %d, want %d", tag, got, want)
		}
	}
}

func TestSQLMD5Password(t *testing.T) {
	// md5(md5(password+user)+salt), prefixed with "md5"; computed by hand.
	got := md5Password("user", "secret", []byte{1, 2, 3, 4})
	if !strings.HasPrefix(got, "md5") || len(got) != 35 {
		t.Fatalf("md5Password = %q", got)
	}
	again := md5Password("user", "secret", []byte{1, 2, 3, 4})
	if got != again {
		t.Fatalf("md5Password is not deterministic")
	}
	if other := md5Password("user", "secret", []byte{4, 3, 2, 1}); other == got {
		t.Fatalf("salt must change the hash")
	}
}

// TestSQLScram exercises the client half against an independent computation of
// the server half (the same formulas PG uses), so a broken client cannot pass.
func TestSQLScram(t *testing.T) {
	const password = "pencil"
	saltB64 := "W22ZaJ0SNY7soEsUEjb6gQ=="
	salt, err := base64.StdEncoding.DecodeString(saltB64)
	if err != nil {
		t.Fatal(err)
	}
	client, err := newScramClient([]byte("SCRAM-SHA-256\x00\x00"), "user", password)
	if err != nil {
		t.Fatal(err)
	}
	clientFirst := client.clientFirst()
	if !strings.HasPrefix(clientFirst, "n,,n=,r=") {
		t.Fatalf("client first: %q", clientFirst)
	}
	nonce := strings.TrimPrefix(clientFirst, "n,,n=,r=")
	serverFirst := "r=" + nonce + "SRVsuffix,s=" + saltB64 + ",i=4096"
	final, err := client.finish(serverFirst)
	if err != nil {
		t.Fatal(err)
	}
	attrs := parseScram(final)
	if attrs["r"] != nonce+"SRVsuffix" {
		t.Fatalf("client-final nonce: %q", attrs["r"])
	}
	proof, err := base64.StdEncoding.DecodeString(attrs["p"])
	if err != nil {
		t.Fatalf("proof is not base64: %v", err)
	}
	salted := pbkdf2SHA256([]byte(password), salt, 4096, 32)
	clientKey := hmacBytes(salted, []byte("Client Key"))
	storedKey := sha256Sum(clientKey)
	authMessage := "n=,r=" + nonce + "," + serverFirst + ",c=biws,r=" + nonce + "SRVsuffix"
	wantProof := xorBytes(clientKey, hmacBytes(storedKey, []byte(authMessage)))
	if !bytes.Equal(proof, wantProof) {
		t.Fatalf("client proof mismatch")
	}
	serverSig := hmacBytes(hmacBytes(salted, []byte("Server Key")), []byte(authMessage))
	if err := client.verifyFinal("v=" + base64.StdEncoding.EncodeToString(serverSig)); err != nil {
		t.Fatalf("verifyFinal: %v", err)
	}
	if err := client.verifyFinal("v=" + base64.StdEncoding.EncodeToString([]byte("bogus"))); err == nil {
		t.Fatalf("verifyFinal accepted a bad server signature")
	}
}

// TestSQLMockServer runs a program against a minimal in-process PostgreSQL
// server on all three engines. It covers startup, the extended query protocol,
// parameter encoding, typed decoding, error responses and capability gating.
func TestSQLMockServer(t *testing.T) {
	srv := startMockPG(t)
	defer srv.Close()

	src := `
import "sql"

struct Item { id: Int, name: Str, price: Float, active: Bool }

fn boom(c: sql.Conn) -> Str uses sql {
    let _ = sql.query(c, text: sql"SELECT boom") catch e { return "${e.code} ${e.message}" }
    return "ok"
}

fn main() -> ! uses sql {
    let c = try sql.open("postgres://u@` + srv.Addr() + `/db?sslmode=disable", timeout_ms: 2000)
    print(try sql.exec(c, text: sql"INSERT INTO t VALUES (1)"))
    print(try sql.exec(c, text: sql"UPDATE t SET a = $1, b = $2", args: [5, "x"]))
    let rows = try sql.query(c, text: sql"SELECT $1, $2", args: [5, "x"])
    print(rows[0]["a"], rows[0]["b"])
    let items = try sql.query_as(c, text: sql"SELECT items", schema: Item)
    print(items[0].id, items[0].name, items[0].price, items[0].active)
    print("boom:", boom(c))
    sql.close(c)
}
`
	out, res := runSrc(t, src, "sql")
	if res.Status != "ok" {
		t.Fatalf("status %s: %s\n%s", res.Status, res.Describe(), out)
	}
	want := "3\n2\n5 x\n7 widget 3.5 true\nboom: E_SQL relation \"boom\" does not exist (SQLSTATE 42P01)\n"
	if out != want {
		t.Fatalf("output\n--- got ---\n%s\n--- want ---\n%s", out, want)
	}
}

func TestSQLCapabilityDenied(t *testing.T) {
	src := `
import "sql"

fn main() -> ! uses sql {
    let c = try sql.open("postgres://u@127.0.0.1:1/db?sslmode=disable")
    sql.close(c)
}
`
	_, res := runSrc(t, src) // no capabilities granted
	if res.ExitCode != ExitPermission {
		t.Fatalf("exit code = %d, want %d (%s)", res.ExitCode, ExitPermission, res.Describe())
	}
}

func TestSQLTLSHandshake(t *testing.T) {
	// A server that answers the SSLRequest with 'S' and then completes a TLS
	// handshake: negotiateTLS must wrap the socket in tls.Conn.
	cert := selfSigned(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		nc, err := ln.Accept()
		if err != nil {
			return
		}
		defer nc.Close()
		var req [8]byte
		if _, err := io.ReadFull(nc, req[:]); err != nil {
			return
		}
		nc.Write([]byte{'S'})
		tc := tls.Server(nc, &tls.Config{Certificates: []tls.Certificate{cert}})
		tc.Handshake()
	}()
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	c := &sqlConn{dsn: sqlDSN{host: host, port: port, sslmode: "require", timeout: 2 * time.Second}}
	raw, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	wrapped, err := c.negotiateTLS(raw)
	if err != nil {
		t.Fatalf("negotiateTLS: %v", err)
	}
	if _, ok := wrapped.(*tls.Conn); !ok {
		t.Fatalf("expected a tls.Conn, got %T", wrapped)
	}
	wrapped.Close()
}

// ---------- minimal mock PostgreSQL server ----------

type mockPG struct {
	ln net.Listener
}

func startMockPG(t *testing.T) *mockPG {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &mockPG{ln: ln}
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(t, nc)
		}
	}()
	return s
}

func (s *mockPG) Addr() string { return s.ln.Addr().String() }
func (s *mockPG) Close()       { s.ln.Close() }

func (s *mockPG) serve(t *testing.T, nc net.Conn) {
	defer nc.Close()
	r := bufio.NewReader(nc)
	// startup (untyped): length + protocol + key/value pairs
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return
	}
	body := make([]byte, binary.BigEndian.Uint32(hdr)-4)
	if _, err := io.ReadFull(r, body); err != nil {
		return
	}
	proto := binary.BigEndian.Uint32(body)
	if proto == 80877103 { // SSLRequest
		nc.Write([]byte{'N'})
		if _, err := io.ReadFull(r, hdr); err != nil {
			return
		}
		body = make([]byte, binary.BigEndian.Uint32(hdr)-4)
		if _, err := io.ReadFull(r, body); err != nil {
			return
		}
	}
	// AuthenticationOk, ParameterStatus, BackendKeyData, ReadyForQuery
	mockWrite(nc, 'R', int32b(0))
	mockWrite(nc, 'S', append(cstr("server_version"), cstr("16.0")...))
	mockWrite(nc, 'K', append(int32b(1234), int32b(5678)...))
	mockWrite(nc, 'Z', []byte{'I'})

	var query string
	var params [][]byte
	for {
		tb := make([]byte, 1)
		if _, err := io.ReadFull(r, tb); err != nil {
			return
		}
		if _, err := io.ReadFull(r, hdr); err != nil {
			return
		}
		payload := make([]byte, binary.BigEndian.Uint32(hdr)-4)
		if _, err := io.ReadFull(r, payload); err != nil {
			return
		}
		switch tb[0] {
		case 'P': // Parse
			_, rest, _ := readCString(payload)
			query, _, _ = readCString(rest)
			mockWrite(nc, '1', nil)
		case 'B': // Bind
			params = mockParseBind(payload)
			mockWrite(nc, '2', nil)
		case 'D': // Describe
			cols, _, _ := mockResult(query, params)
			if cols == nil {
				mockWrite(nc, 'n', nil)
			} else {
				mockWrite(nc, 'T', mockRowDescription(cols))
			}
		case 'E': // Execute
			cols, rows, tag := mockResult(query, params)
			if cols == nil {
				mockWrite(nc, 'E', mockErrorResp("42P01", "relation \""+strings.Fields(query)[1]+"\" does not exist"))
			} else {
				for _, row := range rows {
					mockWrite(nc, 'D', mockDataRow(row))
				}
				mockWrite(nc, 'C', cstr(tag))
			}
		case 'S': // Sync
			mockWrite(nc, 'Z', []byte{'I'})
		case 'X': // Terminate
			return
		}
	}
}

type mockCol struct {
	name string
	oid  uint32
}

// mockResult returns the columns and rows for a query, or a nil column list
// to signal an error (an invalid relation).
func mockResult(q string, params [][]byte) ([]mockCol, [][][]byte, string) {
	switch strings.TrimSpace(q) {
	case "SELECT $1, $2":
		row := make([][]byte, len(params))
		copy(row, params)
		return []mockCol{{"a", 23}, {"b", 25}}, [][][]byte{row}, "SELECT 1"
	case "SELECT $1, $2, $3":
		return []mockCol{{"i", 20}, {"s", 25}, {"b", 16}}, [][][]byte{params}, "SELECT 1"
	case "SELECT items":
		return []mockCol{{"id", 23}, {"name", 25}, {"price", 701}, {"active", 16}},
			[][][]byte{{[]byte("7"), []byte("widget"), []byte("3.5"), []byte("t")}}, "SELECT 1"
	case "SELECT one":
		return []mockCol{{"n", 20}}, [][][]byte{{[]byte("1")}}, "SELECT 1"
	case "INSERT INTO t VALUES (1)":
		return []mockCol{}, nil, "INSERT 0 3"
	case "UPDATE t SET a = $1, b = $2":
		return []mockCol{}, nil, "UPDATE 2"
	}
	return nil, nil, ""
}

func mockParseBind(b []byte) [][]byte {
	_, rest, _ := readCString(b) // portal
	_, rest, _ = readCString(rest)
	nfmt := int(binary.BigEndian.Uint16(rest))
	rest = rest[2+2*nfmt:]
	n := int(binary.BigEndian.Uint16(rest))
	rest = rest[2:]
	out := make([][]byte, 0, n)
	for i := 0; i < n; i++ {
		l := int32(binary.BigEndian.Uint32(rest))
		rest = rest[4:]
		if l < 0 {
			out = append(out, nil)
			continue
		}
		out = append(out, rest[:l])
		rest = rest[l:]
	}
	return out
}

func mockRowDescription(cols []mockCol) []byte {
	var b bytes.Buffer
	writeInt16(&b, int16(len(cols)))
	for _, c := range cols {
		writeCString(&b, c.name)
		writeInt32(&b, 0) // table oid
		writeInt16(&b, 0) // column attr
		writeInt32(&b, int32(c.oid))
		writeInt16(&b, -1) // type size
		writeInt32(&b, -1) // type modifier
		writeInt16(&b, 0)  // text format
	}
	return b.Bytes()
}

func mockDataRow(row [][]byte) []byte {
	var b bytes.Buffer
	writeInt16(&b, int16(len(row)))
	for _, f := range row {
		if f == nil {
			writeInt32(&b, -1)
			continue
		}
		writeInt32(&b, int32(len(f)))
		b.Write(f)
	}
	return b.Bytes()
}

func mockErrorResp(code, msg string) []byte {
	var b bytes.Buffer
	b.WriteByte('S')
	b.Write(cstr("ERROR"))
	b.WriteByte('C')
	b.Write(cstr(code))
	b.WriteByte('M')
	b.Write(cstr(msg))
	b.WriteByte(0)
	return b.Bytes()
}

func mockWrite(w io.Writer, t byte, payload []byte) {
	var hdr [5]byte
	hdr[0] = t
	binary.BigEndian.PutUint32(hdr[1:], uint32(len(payload)+4))
	w.Write(hdr[:])
	w.Write(payload)
}

func cstr(s string) []byte {
	out := make([]byte, 0, len(s)+1)
	out = append(out, s...)
	return append(out, 0)
}

func int32b(v uint32) []byte {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	return b[:]
}

func sha256Sum(b []byte) []byte {
	s := sha256.Sum256(b)
	return s[:]
}

// selfSigned builds an in-memory certificate for the TLS handshake test.
func selfSigned(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}
