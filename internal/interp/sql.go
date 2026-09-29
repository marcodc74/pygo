package interp

import (
	"bufio"
	"bytes"
	"crypto/hmac"
	"crypto/md5" //nolint:gosec // required byte-for-byte by the PostgreSQL MD5 auth exchange
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/marcodc74/pygo/internal/ast"
)

// sql implements the `sql` stdlib module: a small, dependency-free PostgreSQL
// client. It speaks the wire protocol v3 directly over net.Conn (no CGO, no
// third-party driver) and runs statements with the extended query protocol so
// that parameters never become SQL. Everything is gated by the `sql`
// capability. Connections are handles (struct sql.Conn) opened lazily on the
// first query; the interpreter keeps the live sockets in a per-Interp registry,
// so ids and behaviour are reproducible.
func init() {
	register("sql", map[string]BuiltinFn{
		"open":     sqlOpen,
		"close":    sqlClose,
		"query":    sqlQuery,
		"query_as": sqlQueryAs,
		"exec":     sqlExec,
	})
}

// ---------- registry ----------

func (in *Interp) sqlConnType() *StructType {
	return in.stdModule("sql").Types["Conn"].(*StructType)
}

func (in *Interp) addSQLConn(c *sqlConn) *Struct {
	in.sqlMu.Lock()
	in.sqlSeq++
	id := in.sqlSeq
	in.sqlConns[id] = c
	in.sqlMu.Unlock()
	return &Struct{T: in.sqlConnType(), F: []Value{id}}
}

// sqlConnHandle returns the id of a sql.Conn value, checking only its type.
func (in *Interp) sqlConnHandle(v Value) (int64, bool) {
	s, ok := v.(*Struct)
	if !ok || s.T != in.sqlConnType() {
		return 0, false
	}
	f, ok := s.Field("id")
	if !ok {
		return 0, false
	}
	id, ok := f.(int64)
	return id, ok
}

func (in *Interp) sqlConnByID(id int64) *sqlConn {
	in.sqlMu.Lock()
	defer in.sqlMu.Unlock()
	return in.sqlConns[id]
}

func (in *Interp) dropSQLConn(id int64) {
	in.sqlMu.Lock()
	delete(in.sqlConns, id)
	in.sqlMu.Unlock()
}

// closeAllSQL closes every live connection (called when the run ends).
func (in *Interp) closeAllSQL() {
	in.sqlMu.Lock()
	conns := in.sqlConns
	in.sqlConns = map[int64]*sqlConn{}
	in.sqlMu.Unlock()
	for _, c := range conns {
		c.close()
	}
}

// ---------- builtins ----------

func sqlOpen(th *Thread, _ Value, a []Value) (Value, error) {
	d, err := parseDSN(a[0].(string))
	if err != nil {
		return nil, th.fail("E_SQL", "%v", err)
	}
	if ms := a[1].(int64); ms > 0 {
		d.timeout = time.Duration(ms) * time.Millisecond
	} else if ms < 0 {
		return nil, perr(PArgs, "", "sql.open: timeout_ms must not be negative")
	}
	return th.in.addSQLConn(&sqlConn{dsn: d}), nil
}

func sqlClose(th *Thread, _ Value, a []Value) (Value, error) {
	id, ok := th.in.sqlConnHandle(a[0])
	if !ok {
		return nil, perr(PType, "", "sql.close expects a sql.Conn from sql.open")
	}
	if c := th.in.sqlConnByID(id); c != nil {
		c.close()
	}
	th.in.dropSQLConn(id)
	return nil, nil
}

func sqlQuery(th *Thread, _ Value, a []Value) (Value, error) {
	return th.sqlRows(a[0], a[1], nil, a[2], a[3])
}

func sqlQueryAs(th *Thread, _ Value, a []Value) (Value, error) {
	return th.sqlRows(a[0], a[1], a[2], a[3], a[4])
}

func sqlExec(th *Thread, _ Value, a []Value) (Value, error) {
	id, ok := th.in.sqlConnHandle(a[0])
	if !ok {
		return nil, perr(PType, "", "sql.exec expects a sql.Conn from sql.open")
	}
	c := th.in.sqlConnByID(id)
	if c == nil {
		return nil, th.fail("E_SQL", "connection is closed")
	}
	args := a[2].(*List).Snapshot()
	text, msg := prepareSQL(a[1].(SqlStr), args)
	if msg != "" {
		return nil, th.fail("E_SQL", "%s", msg)
	}
	timeout, terr := msDuration(a[3])
	if terr != nil {
		return nil, terr
	}
	_, _, tag, err := c.run(text, args, timeout)
	if err != nil {
		return nil, th.fail("E_SQL", "%v", err)
	}
	return commandRows(tag), nil
}

// sqlRows runs a SELECT and returns rows as maps (schema == nil) or decoded
// into the caller's type (schema != nil, query_as).
func (th *Thread) sqlRows(connV, textV, schemaV, argsV, timeoutV Value) (Value, error) {
	id, ok := th.in.sqlConnHandle(connV)
	if !ok {
		return nil, perr(PType, "", "sql.query expects a sql.Conn from sql.open")
	}
	c := th.in.sqlConnByID(id)
	if c == nil {
		return nil, th.fail("E_SQL", "connection is closed")
	}
	args := argsV.(*List).Snapshot()
	text, perrMsg := prepareSQL(textV.(SqlStr), args)
	if perrMsg != "" {
		return nil, th.fail("E_SQL", "%s", perrMsg)
	}
	timeout, terr := msDuration(timeoutV)
	if terr != nil {
		return nil, terr
	}
	cols, rows, _, err := c.run(text, args, timeout)
	if err != nil {
		return nil, th.fail("E_SQL", "%v", err)
	}
	out := make([]Value, len(rows))
	for i, row := range rows {
		m := NewMap()
		for j, f := range row {
			if j < len(cols) {
				m.Set(cols[j].name, decodeSQL(f, cols[j].oid))
			}
		}
		out[i] = m
	}
	if schemaV == nil {
		return NewList(out), nil
	}
	te, mod, serr := schemaExpr(schemaV)
	if serr != "" {
		return nil, perr(PType, "", "%s", serr)
	}
	typed := make([]Value, len(out))
	for i, m := range out {
		v, cerr := th.convert(m, te, mod, "$")
		if cerr != "" {
			return nil, th.fail("E_SCHEMA", "%s", cerr)
		}
		typed[i] = v
	}
	return NewList(typed), nil
}

func msDuration(v Value) (time.Duration, error) {
	ms := v.(int64)
	if ms < 0 {
		return 0, perr(PArgs, "", "timeout_ms must not be negative")
	}
	if ms == 0 {
		return 0, nil
	}
	return time.Duration(ms) * time.Millisecond, nil
}

// schemaExpr turns a type value (User, Shape, ...) into what convert needs.
func schemaExpr(v Value) (*ast.TypeExpr, *Module, string) {
	switch t := v.(type) {
	case *StructType:
		return &ast.TypeExpr{Name: t.Name}, t.Module, ""
	case *EnumType:
		return &ast.TypeExpr{Name: t.Name}, t.Module, ""
	}
	return nil, nil, fmt.Sprintf("schema must be a struct or enum type, got %s", TypeName(v))
}

// ---------- placeholders ----------

// prepareSQL returns the query to send and a message when the placeholders and
// the arguments do not line up. Native $1..$n placeholders are left as they
// are; a query that uses no $ is allowed to use ? and gets rewritten to
// $1..$n, so a bound value is never concatenated into the text.
func prepareSQL(text SqlStr, args []Value) (string, string) {
	q := string(text)
	qmarks, hasDollar := scanSQL(q)
	// Native $n (or a query with no arguments, e.g. the jsonb ? operator) is
	// sent as written. Only a query that uses ? and does pass arguments is
	// rewritten, so a bound value is never concatenated into the text.
	if hasDollar || len(args) == 0 {
		return q, ""
	}
	if len(qmarks) == 0 {
		return "", "query has no placeholders but arguments were given"
	}
	if len(qmarks) != len(args) {
		return "", fmt.Sprintf("query has %d ? placeholders but %d argument(s) were given", len(qmarks), len(args))
	}
	var b strings.Builder
	prev := 0
	for i, pos := range qmarks {
		b.WriteString(q[prev:pos])
		fmt.Fprintf(&b, "$%d", i+1)
		prev = pos + 1
	}
	b.WriteString(q[prev:])
	return b.String(), ""
}

// scanSQL returns the offsets of '?' outside strings and comments, and whether
// a '$' placeholder (or dollar-quote) appears outside them.
func scanSQL(q string) ([]int, bool) {
	var qmarks []int
	hasDollar := false
	i, n := 0, len(q)
	for i < n {
		switch c := q[i]; {
		case c == '\'':
			i++
			for i < n {
				if q[i] == '\'' {
					if i+1 < n && q[i+1] == '\'' {
						i += 2
						continue
					}
					i++
					break
				}
				i++
			}
		case c == '"':
			i++
			for i < n {
				if q[i] == '"' {
					i++
					break
				}
				i++
			}
		case c == '$':
			if end := dollarTag(q, i); end > i {
				tag := q[i:end]
				if closeIdx := strings.Index(q[end:], tag); closeIdx >= 0 {
					i = end + closeIdx + len(tag)
					continue
				}
			}
			hasDollar = true
			i++
		case c == '-' && i+1 < n && q[i+1] == '-':
			for i < n && q[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < n && q[i+1] == '*':
			depth := 1
			i += 2
			for i < n && depth > 0 {
				if i+1 < n && q[i] == '/' && q[i+1] == '*' {
					depth++
					i += 2
					continue
				}
				if i+1 < n && q[i] == '*' && q[i+1] == '/' {
					depth--
					i += 2
					continue
				}
				i++
			}
		case c == '?':
			qmarks = append(qmarks, i)
			i++
		default:
			i++
		}
	}
	return qmarks, hasDollar
}

// dollarTag reports the end of a $tag$ opener at q[i], or i when it is not one.
func dollarTag(q string, i int) int {
	if i >= len(q) || q[i] != '$' {
		return i
	}
	j := i + 1
	if j < len(q) && q[j] == '$' {
		return j + 1
	}
	if j < len(q) && (isSQLAlpha(q[j]) || q[j] == '_') {
		for j < len(q) && (isSQLAlpha(q[j]) || isSQLDigit(q[j]) || q[j] == '_') {
			j++
		}
		if j < len(q) && q[j] == '$' {
			return j + 1
		}
	}
	return i
}

func isSQLAlpha(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
func isSQLDigit(c byte) bool { return c >= '0' && c <= '9' }

// ---------- connection string ----------

type sqlDSN struct {
	host, port             string
	user, password, dbname string
	sslmode                string
	timeout                time.Duration
}

func parseDSN(s string) (sqlDSN, error) {
	d := sqlDSN{host: "localhost", port: "5432", sslmode: "prefer"}
	s = strings.TrimSpace(s)
	if s == "" {
		return d, fmt.Errorf("empty connection string")
	}
	if strings.HasPrefix(s, "postgres://") || strings.HasPrefix(s, "postgresql://") {
		u, err := url.Parse(s)
		if err != nil {
			return d, fmt.Errorf("invalid connection URL: %v", err)
		}
		if u.Hostname() != "" {
			d.host = u.Hostname()
		}
		if p := u.Port(); p != "" {
			d.port = p
		}
		if u.User != nil {
			d.user = u.User.Username()
			if pw, ok := u.User.Password(); ok {
				d.password = pw
			}
		}
		d.dbname = strings.TrimPrefix(u.Path, "/")
		if dec, err := url.PathUnescape(d.dbname); err == nil {
			d.dbname = dec
		}
		q := u.Query()
		if v := q.Get("host"); v != "" {
			d.host = v
		}
		if v := q.Get("port"); v != "" {
			d.port = v
		}
		if v := q.Get("user"); v != "" {
			d.user = v
		}
		if v := q.Get("password"); v != "" {
			d.password = v
		}
		if v := q.Get("dbname"); v != "" {
			d.dbname = v
		}
		if v := q.Get("sslmode"); v != "" {
			d.sslmode = v
		}
	} else {
		kv, err := parseConnInfo(s)
		if err != nil {
			return d, err
		}
		for k, v := range kv {
			switch k {
			case "host", "hostaddr":
				d.host = v
			case "port":
				d.port = v
			case "user":
				d.user = v
			case "password":
				d.password = v
			case "dbname", "database":
				d.dbname = v
			case "sslmode":
				d.sslmode = v
			}
		}
	}
	switch d.sslmode {
	case "disable", "allow", "prefer", "require", "verify-ca", "verify-full":
	default:
		return d, fmt.Errorf("unsupported sslmode %q (use disable, allow, prefer, require, verify-ca or verify-full)", d.sslmode)
	}
	if d.timeout <= 0 {
		d.timeout = 5 * time.Second
	}
	return d, nil
}

// parseConnInfo parses libpq key=value pairs ("host=a user=b dbname=c"),
// with single-quoted values and backslash escapes.
func parseConnInfo(s string) (map[string]string, error) {
	out := map[string]string{}
	i, n := 0, len(s)
	for i < n {
		for i < n && isSpace(s[i]) {
			i++
		}
		if i >= n {
			break
		}
		start := i
		for i < n && s[i] != '=' && !isSpace(s[i]) {
			i++
		}
		if i >= n || s[i] != '=' {
			return nil, fmt.Errorf("invalid connection string: expected key=value, found %q", strings.TrimSpace(s[start:]))
		}
		key := strings.ToLower(s[start:i])
		i++
		var val strings.Builder
		if i < n && s[i] == '\'' {
			i++
			for i < n {
				if s[i] == '\\' && i+1 < n {
					val.WriteByte(s[i+1])
					i += 2
					continue
				}
				if s[i] == '\'' {
					i++
					break
				}
				val.WriteByte(s[i])
				i++
			}
		} else {
			for i < n && !isSpace(s[i]) {
				if s[i] == '\\' && i+1 < n {
					val.WriteByte(s[i+1])
					i += 2
					continue
				}
				val.WriteByte(s[i])
				i++
			}
		}
		out[key] = val.String()
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("invalid connection string")
	}
	return out, nil
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

// ---------- the connection ----------

type sqlConn struct {
	dsn  sqlDSN
	mu   sync.Mutex
	netc net.Conn
	r    *bufio.Reader
	w    *bufio.Writer
}

func (c *sqlConn) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.broken()
}

// broken drops the socket after a transport error (holds c.mu).
func (c *sqlConn) broken() {
	if c.netc != nil {
		c.netc.Close()
	}
	c.netc, c.r, c.w = nil, nil, nil
}

// run executes one statement with the extended query protocol and returns the
// column descriptions, text rows and command tag.
func (c *sqlConn) run(text string, args []Value, timeout time.Duration) (cols []sqlField, rows []sqlRow, tag string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.netc == nil {
		if err = c.connect(); err != nil {
			c.broken()
			return nil, nil, "", err
		}
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	_ = c.netc.SetDeadline(time.Now().Add(timeout))
	if err = c.writeQuery(text, args); err != nil {
		c.broken()
		return nil, nil, "", err
	}
	return c.readResults()
}

func (c *sqlConn) connect() error {
	dialer := net.Dialer{Timeout: c.dsn.timeout}
	nc, err := dialer.Dial("tcp", net.JoinHostPort(c.dsn.host, c.dsn.port))
	if err != nil {
		return err
	}
	// Bound TLS negotiation, startup and authentication too: a server that
	// accepts and then goes silent must not hang the program.
	_ = nc.SetDeadline(time.Now().Add(c.dsn.timeout))
	nc2, err := c.negotiateTLS(nc)
	if err != nil {
		nc.Close()
		return err
	}
	nc = nc2
	c.netc = nc
	c.r = bufio.NewReader(nc)
	c.w = bufio.NewWriter(nc)
	if err := c.sendStartup(); err != nil {
		c.broken()
		return err
	}
	if err := c.auth(); err != nil {
		c.broken()
		return err
	}
	return nil
}

// negotiateTLS performs the SSLRequest round trip. sslmode=prefer/allow falls
// back to plaintext when the server says no.
func (c *sqlConn) negotiateTLS(nc net.Conn) (net.Conn, error) {
	if c.dsn.sslmode == "disable" {
		return nc, nil
	}
	req := []byte{0, 0, 0, 8, 0x04, 0xd2, 0x16, 0x2f} // SSLRequest
	if _, err := nc.Write(req); err != nil {
		return nil, err
	}
	var b [1]byte
	if _, err := io.ReadFull(nc, b[:]); err != nil {
		return nil, err
	}
	switch b[0] {
	case 'S':
		cfg := &tls.Config{MinVersion: tls.VersionTLS12}
		switch c.dsn.sslmode {
		case "require", "prefer", "allow":
			cfg.InsecureSkipVerify = true
		default: // verify-ca, verify-full
			cfg.ServerName = c.dsn.host
		}
		tc := tls.Client(nc, cfg)
		if err := tc.Handshake(); err != nil {
			return nil, err
		}
		return tc, nil
	case 'N':
		if c.dsn.sslmode == "require" || c.dsn.sslmode == "verify-ca" || c.dsn.sslmode == "verify-full" {
			return nil, fmt.Errorf("server does not support TLS but sslmode=%s", c.dsn.sslmode)
		}
		return nc, nil
	default:
		return nil, fmt.Errorf("unexpected SSL response %q", b[0])
	}
}

func (c *sqlConn) sendStartup() error {
	var b bytes.Buffer
	writeInt32(&b, 196608) // protocol 3.0
	writeCString(&b, "user")
	writeCString(&b, c.dsn.user)
	if c.dsn.dbname != "" {
		writeCString(&b, "database")
		writeCString(&b, c.dsn.dbname)
	}
	writeCString(&b, "application_name")
	writeCString(&b, "pygo")
	writeCString(&b, "client_encoding")
	writeCString(&b, "UTF8")
	b.WriteByte(0)
	return c.sendUntyped(b.Bytes())
}

// sendUntyped writes a startup-phase message (length + payload, no type byte).
func (c *sqlConn) sendUntyped(payload []byte) error {
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(payload)+4))
	if _, err := c.w.Write(hdr[:]); err != nil {
		return err
	}
	if _, err := c.w.Write(payload); err != nil {
		return err
	}
	return c.w.Flush()
}

// sendTyped writes a typed frontend message.
func (c *sqlConn) sendTyped(t byte, payload []byte) error {
	var hdr [5]byte
	hdr[0] = t
	binary.BigEndian.PutUint32(hdr[1:], uint32(len(payload)+4))
	if _, err := c.w.Write(hdr[:]); err != nil {
		return err
	}
	if _, err := c.w.Write(payload); err != nil {
		return err
	}
	return c.w.Flush()
}

func (c *sqlConn) readMsg() (byte, []byte, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(c.r, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := int(binary.BigEndian.Uint32(hdr[1:]))
	if n < 4 || n > 64<<20 {
		return 0, nil, fmt.Errorf("bad message length %d", n)
	}
	payload := make([]byte, n-4)
	if _, err := io.ReadFull(c.r, payload); err != nil {
		return 0, nil, err
	}
	return hdr[0], payload, nil
}

// ---------- authentication ----------

func (c *sqlConn) auth() error {
	var scram *scramClient
	for {
		t, payload, err := c.readMsg()
		if err != nil {
			return err
		}
		switch t {
		case 'R':
			if len(payload) < 4 {
				return fmt.Errorf("malformed authentication message")
			}
			code := binary.BigEndian.Uint32(payload)
			data := payload[4:]
			switch code {
			case 0: // AuthenticationOk
			case 3: // cleartext
				if err := c.sendPassword([]byte(c.dsn.password)); err != nil {
					return err
				}
			case 5: // MD5
				if len(data) < 4 {
					return fmt.Errorf("malformed MD5 salt")
				}
				if err := c.sendPassword([]byte(md5Password(c.dsn.user, c.dsn.password, data[:4]))); err != nil {
					return err
				}
			case 10: // SASL
				if scram, err = newScramClient(payload[4:], c.dsn.user, c.dsn.password); err != nil {
					return err
				}
				if err := c.sendSASLInitial(scram.clientFirst()); err != nil {
					return err
				}
			case 11: // SASL continue
				if scram == nil {
					return fmt.Errorf("unexpected SASL continue")
				}
				final, ferr := scram.finish(string(payload[4:]))
				if ferr != nil {
					return ferr
				}
				if err := c.sendSASLResponse(final); err != nil {
					return err
				}
			case 12: // SASL final
				if scram == nil {
					return fmt.Errorf("unexpected SASL final")
				}
				if err := scram.verifyFinal(string(payload[4:])); err != nil {
					return err
				}
			default:
				return fmt.Errorf("server requested unsupported authentication %d", code)
			}
		case 'S', 'K', 'N': // ParameterStatus, BackendKeyData, Notice: ignored
		case 'E':
			return pgError(payload)
		case 'Z':
			return nil
		default:
			return fmt.Errorf("unexpected message %q during startup", t)
		}
	}
}

func (c *sqlConn) sendPassword(pw []byte) error {
	return c.sendTyped('p', append(pw, 0))
}

func (c *sqlConn) sendSASLInitial(initial string) error {
	var b bytes.Buffer
	writeCString(&b, "SCRAM-SHA-256")
	writeInt32(&b, int32(len(initial)))
	b.WriteString(initial)
	return c.sendTyped('p', b.Bytes())
}

func (c *sqlConn) sendSASLResponse(resp string) error {
	return c.sendTyped('p', []byte(resp))
}

func md5Password(user, password string, salt []byte) string {
	inner := md5.Sum([]byte(password + user))
	h := hex.EncodeToString(inner[:])
	outer := md5.Sum(append([]byte(h), salt...))
	return "md5" + hex.EncodeToString(outer[:])
}

// scramClient is the client half of SCRAM-SHA-256 (RFC 5802/7677).
type scramClient struct {
	password        string
	nonce           string
	clientFirstBare string
	serverSig       []byte
}

func newScramClient(mechs []byte, user, password string) (*scramClient, error) {
	offered := strings.Split(strings.TrimRight(string(mechs), "\x00"), "\x00")
	ok := false
	for _, m := range offered {
		if m == "SCRAM-SHA-256" {
			ok = true
		}
	}
	if !ok {
		return nil, fmt.Errorf("server offers no supported SASL mechanism (have %q, need SCRAM-SHA-256)", offered)
	}
	nb := make([]byte, 18)
	if _, err := rand.Read(nb); err != nil {
		return nil, err
	}
	s := &scramClient{password: password, nonce: base64.StdEncoding.EncodeToString(nb)}
	s.clientFirstBare = "n=,r=" + s.nonce
	return s, nil
}

func (s *scramClient) clientFirst() string { return "n,," + s.clientFirstBare }

func (s *scramClient) finish(serverFirst string) (string, error) {
	attrs := parseScram(serverFirst)
	nonce := attrs["r"]
	if !strings.HasPrefix(nonce, s.nonce) {
		return "", fmt.Errorf("scram: server nonce does not extend the client nonce")
	}
	salt, err := base64.StdEncoding.DecodeString(attrs["s"])
	if err != nil {
		return "", fmt.Errorf("scram: invalid salt")
	}
	iters, err := strconv.Atoi(attrs["i"])
	if err != nil || iters < 1 {
		return "", fmt.Errorf("scram: invalid iteration count")
	}
	salted := pbkdf2SHA256([]byte(s.password), salt, iters, sha256.Size)
	clientKey := hmacBytes(salted, []byte("Client Key"))
	storedKey := sha256.Sum256(clientKey)
	clientFinal := "c=biws,r=" + nonce
	authMessage := s.clientFirstBare + "," + serverFirst + "," + clientFinal
	proof := xorBytes(clientKey, hmacBytes(storedKey[:], []byte(authMessage)))
	s.serverSig = hmacBytes(hmacBytes(salted, []byte("Server Key")), []byte(authMessage))
	return clientFinal + ",p=" + base64.StdEncoding.EncodeToString(proof), nil
}

func (s *scramClient) verifyFinal(serverFinal string) error {
	v, err := base64.StdEncoding.DecodeString(parseScram(serverFinal)["v"])
	if err != nil {
		return fmt.Errorf("scram: invalid server signature")
	}
	if !hmac.Equal(v, s.serverSig) {
		return fmt.Errorf("scram: server signature mismatch")
	}
	return nil
}

func parseScram(s string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(s, ",") {
		if i := strings.IndexByte(part, '='); i > 0 {
			out[part[:i]] = part[i+1:]
		}
	}
	return out
}

func hmacBytes(key, data []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(data)
	return m.Sum(nil)
}

func xorBytes(a, b []byte) []byte {
	out := make([]byte, len(a))
	for i := range a {
		out[i] = a[i] ^ b[i]
	}
	return out
}

// ---------- query execution ----------

func (c *sqlConn) writeQuery(text string, args []Value) error {
	if len(args) > 32767 {
		return fmt.Errorf("too many parameters (%d; the protocol limit is 32767)", len(args))
	}
	var out bytes.Buffer
	// Parse (unnamed statement, inferred parameter types)
	var p bytes.Buffer
	writeCString(&p, "")
	writeCString(&p, text)
	writeInt16(&p, 0)
	writeMsg(&out, 'P', p.Bytes())
	// Bind (unnamed portal, all text parameters and results)
	var b bytes.Buffer
	writeCString(&b, "")
	writeCString(&b, "")
	writeInt16(&b, 0)
	writeInt16(&b, int16(len(args)))
	for _, a := range args {
		enc, err := encodeSQLArg(a)
		if err != nil {
			return err
		}
		if enc == nil {
			writeInt32(&b, -1)
			continue
		}
		writeInt32(&b, int32(len(enc)))
		b.Write(enc)
	}
	writeInt16(&b, 0)
	writeMsg(&out, 'B', b.Bytes())
	// Describe (portal)
	var d bytes.Buffer
	d.WriteByte('P')
	writeCString(&d, "")
	writeMsg(&out, 'D', d.Bytes())
	// Execute
	var e bytes.Buffer
	writeCString(&e, "")
	writeInt32(&e, 0)
	writeMsg(&out, 'E', e.Bytes())
	// Sync
	writeMsg(&out, 'S', nil)
	if _, err := c.w.Write(out.Bytes()); err != nil {
		return err
	}
	return c.w.Flush()
}

func encodeSQLArg(v Value) ([]byte, error) {
	switch x := v.(type) {
	case nil:
		return nil, nil
	case int64:
		return []byte(strconv.FormatInt(x, 10)), nil
	case float64:
		return []byte(FormatFloat(x)), nil
	case bool:
		if x {
			return []byte("t"), nil
		}
		return []byte("f"), nil
	case string:
		return []byte(x), nil
	case HtmlStr:
		return []byte(string(x)), nil
	case SqlStr:
		return []byte(string(x)), nil
	}
	return nil, fmt.Errorf("unsupported parameter type %s (use Int, Float, Bool, Str or nil)", TypeName(v))
}

func (c *sqlConn) readResults() (cols []sqlField, rows []sqlRow, tag string, err error) {
	var pgErr error
	for {
		t, payload, rerr := c.readMsg()
		if rerr != nil {
			c.broken()
			return nil, nil, "", rerr
		}
		switch t {
		case '1', '2', '3', 'n', 's', 'S', 'N', 'A': // complete/NoData/status/notice
		case 'T':
			if cols, err = parseRowDescription(payload); err != nil {
				return nil, nil, "", err
			}
		case 'D':
			if cols != nil {
				row, derr := parseDataRow(payload)
				if derr != nil {
					return nil, nil, "", derr
				}
				rows = append(rows, row)
			}
		case 'C':
			tag = cstring(payload)
		case 'E':
			if pgErr == nil {
				pgErr = pgError(payload)
			}
		case 'Z':
			if pgErr != nil {
				return nil, nil, "", pgErr
			}
			return cols, rows, tag, nil
		default:
			return nil, nil, "", fmt.Errorf("unexpected message %q", t)
		}
	}
}

type sqlField struct {
	name string
	oid  uint32
}

type sqlRow [][]byte

func parseRowDescription(b []byte) ([]sqlField, error) {
	if len(b) < 2 {
		return nil, fmt.Errorf("malformed RowDescription")
	}
	n := int(binary.BigEndian.Uint16(b))
	rest := b[2:]
	out := make([]sqlField, 0, n)
	for i := 0; i < n; i++ {
		name, r, ok := readCString(rest)
		if !ok || len(r) < 18 {
			return nil, fmt.Errorf("malformed RowDescription field")
		}
		out = append(out, sqlField{name: name, oid: binary.BigEndian.Uint32(r[6:10])})
		rest = r[18:]
	}
	return out, nil
}

func parseDataRow(b []byte) (sqlRow, error) {
	if len(b) < 2 {
		return nil, fmt.Errorf("malformed DataRow")
	}
	n := int(binary.BigEndian.Uint16(b))
	rest := b[2:]
	row := make(sqlRow, 0, n)
	for i := 0; i < n; i++ {
		if len(rest) < 4 {
			return nil, fmt.Errorf("malformed DataRow field")
		}
		l := int32(binary.BigEndian.Uint32(rest))
		rest = rest[4:]
		if l < 0 {
			row = append(row, nil)
			continue
		}
		if int(l) > len(rest) {
			return nil, fmt.Errorf("malformed DataRow length")
		}
		row = append(row, rest[:l])
		rest = rest[l:]
	}
	return row, nil
}

// decodeSQL maps a text-format value to an Int, Float, Bool or Str.
func decodeSQL(b []byte, oid uint32) Value {
	if b == nil {
		return nil
	}
	s := string(b)
	switch oid {
	case 16: // bool
		return s == "t"
	case 20, 21, 23: // int8, int2, int4
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return n
		}
		return s
	case 700, 701: // float4, float8
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return f
		}
		return s
	case 1700: // numeric
		if !strings.ContainsAny(s, ".eE") {
			if n, err := strconv.ParseInt(s, 10, 64); err == nil {
				return n
			}
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return f
		}
		return s
	default: // text, varchar, dates, json, uuid, bytea, arrays, ...
		return s
	}
}

// commandRows extracts the affected-row count from a CommandComplete tag.
func commandRows(tag string) int64 {
	f := strings.Fields(tag)
	if len(f) == 0 {
		return 0
	}
	if n, err := strconv.ParseInt(f[len(f)-1], 10, 64); err == nil {
		return n
	}
	return 0
}

func pgError(payload []byte) error {
	fields := map[byte]string{}
	for len(payload) > 0 && payload[0] != 0 {
		code := payload[0]
		s, rest, ok := readCString(payload[1:])
		if !ok {
			break
		}
		fields[code] = s
		payload = rest
	}
	msg := fields['M']
	if msg == "" {
		msg = "unknown server error"
	}
	if state := fields['C']; state != "" {
		return fmt.Errorf("%s (SQLSTATE %s)", msg, state)
	}
	return fmt.Errorf("%s", msg)
}

// ---------- wire helpers ----------

func writeMsg(b *bytes.Buffer, t byte, payload []byte) {
	b.WriteByte(t)
	writeInt32(b, int32(len(payload)+4))
	b.Write(payload)
}

func writeInt16(b *bytes.Buffer, v int16) { _ = binary.Write(b, binary.BigEndian, v) }
func writeInt32(b *bytes.Buffer, v int32) { _ = binary.Write(b, binary.BigEndian, v) }

func writeCString(b *bytes.Buffer, s string) {
	b.WriteString(s)
	b.WriteByte(0)
}

func cstring(b []byte) string {
	s, _, _ := readCString(b)
	return s
}

// readCString reads a NUL-terminated string and reports whether it was found.
func readCString(b []byte) (string, []byte, bool) {
	for i, c := range b {
		if c == 0 {
			return string(b[:i]), b[i+1:], true
		}
	}
	return "", nil, false
}
