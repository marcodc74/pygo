package interp

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/marcodc74/pygo/internal/diag"
	"github.com/marcodc74/pygo/internal/loader"
)

// ---------- helpers ----------

// tlsThread loads a trivial program so a Thread with the stdlib modules exists.
func tlsThread(t *testing.T) *Thread {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "main.pg")
	os.WriteFile(p, []byte("fn main() {}\n"), 0o644)
	prog, ds := loader.Load(p, nil)
	if diag.HasErrors(ds) {
		t.Fatalf("parse errors: %v", ds)
	}
	var out bytes.Buffer
	in := New(prog, Options{Stdout: &out, Stderr: &out})
	m, err := in.Load(prog.Main)
	if err != nil {
		t.Fatal(err)
	}
	return in.newThread(m)
}

// tlsValue builds an http.Tls{cert, key} value.
func tlsValue(t *testing.T, th *Thread, cert, key string) Value {
	t.Helper()
	st, ok := th.in.stdModule("http").Types["Tls"].(*StructType)
	if !ok {
		t.Fatal("http.Tls type not found")
	}
	return &Struct{T: st, F: []Value{cert, key}}
}

// writePEM writes one PEM block.
func writePEM(t *testing.T, path, typ string, der []byte) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := pem.Encode(f, &pem.Block{Type: typ, Bytes: der}); err != nil {
		t.Fatal(err)
	}
}

// writeSelfSigned writes a self-signed certificate for localhost/127.0.0.1 to
// certPath/keyPath, stamps both with mod (to drive reload), and returns a pool
// that trusts it.
func writeSelfSigned(t *testing.T, certPath, keyPath string, serial int64, mod time.Time) *x509.CertPool {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(serial),
		Subject:               pkix.Name{CommonName: "localhost"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	writePEM(t, certPath, "CERTIFICATE", der)
	kb, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	writePEM(t, keyPath, "PRIVATE KEY", kb)
	if err := os.Chtimes(certPath, mod, mod); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(keyPath, mod, mod); err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return pool
}

func mustCert(t *testing.T, r *certReloader) *tls.Certificate {
	t.Helper()
	c, err := r.GetCertificate(nil)
	if err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	return c
}

func certSerial(t *testing.T, c *tls.Certificate) int64 {
	t.Helper()
	leaf, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	return leaf.SerialNumber.Int64()
}

// startTLS serves handler with cfg on 127.0.0.1 and returns its https URL.
func startTLS(t *testing.T, cfg *tls.Config, handler http.Handler) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: handler, TLSConfig: cfg}
	go srv.ServeTLS(ln, "", "")
	t.Cleanup(func() { srv.Close() })
	return "https://" + ln.Addr().String() + "/"
}

// httpsGet fetches url trusting pool and returns the body, the served
// certificate serial and the negotiated protocol version.
func httpsGet(t *testing.T, pool *x509.CertPool, url string) (string, int64, int) {
	t.Helper()
	tr := &http.Transport{
		TLSClientConfig:   &tls.Config{RootCAs: pool, ServerName: "localhost"},
		DisableKeepAlives: true,
	}
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.TLS == nil || len(resp.TLS.PeerCertificates) == 0 {
		t.Fatal("no peer certificate")
	}
	return string(b), resp.TLS.PeerCertificates[0].SerialNumber.Int64(), resp.ProtoMajor
}

// ---------- tests ----------

func TestCertReloader(t *testing.T) {
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "c.pem"), filepath.Join(dir, "c.key")
	writeSelfSigned(t, cert, key, 1, time.Now().Add(time.Second))

	r, err := newCertReloader(cert, key)
	if err != nil {
		t.Fatalf("newCertReloader: %v", err)
	}
	if got := certSerial(t, mustCert(t, r)); got != 1 {
		t.Fatalf("initial serial %d, want 1", got)
	}

	// a renewed pair with a newer mtime is picked up
	writeSelfSigned(t, cert, key, 2, time.Now().Add(2*time.Second))
	if got := certSerial(t, mustCert(t, r)); got != 2 {
		t.Fatalf("after reload serial %d, want 2", got)
	}

	// a broken replacement keeps the last good certificate
	if err := os.WriteFile(cert, []byte("not a certificate"), 0o644); err != nil {
		t.Fatal(err)
	}
	bump := time.Now().Add(3 * time.Second)
	if err := os.Chtimes(cert, bump, bump); err != nil {
		t.Fatal(err)
	}
	if got := certSerial(t, mustCert(t, r)); got != 2 {
		t.Fatalf("broken replacement served serial %d, want the last good 2", got)
	}

	// missing files fail at construction
	if _, err := newCertReloader(filepath.Join(dir, "no.pem"), filepath.Join(dir, "no.key")); err == nil {
		t.Fatal("want an error for missing files")
	}
}

// concurrent handshakes and renewals must not race on the cached pair.
func TestCertReloaderConcurrent(t *testing.T) {
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "c.pem"), filepath.Join(dir, "c.key")
	writeSelfSigned(t, cert, key, 1, time.Now())
	r, err := newCertReloader(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					r.GetCertificate(nil)
				}
			}
		}()
	}
	for i := int64(2); i <= 6; i++ {
		writeSelfSigned(t, cert, key, i, time.Now().Add(time.Duration(i)*time.Second))
	}
	close(stop)
	wg.Wait()
}

func TestHTTPTLSConfig(t *testing.T) {
	th := tlsThread(t)
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "c.pem"), filepath.Join(dir, "c.key")
	writeSelfSigned(t, cert, key, 1, time.Now())

	// wrong value type: a programmer panic, not a failure
	if _, err := th.httpTLSConfig("nope"); err == nil {
		t.Fatal("want a panic for a non-Tls value")
	} else if p, ok := err.(*Panic); !ok || p.Code != PType {
		t.Fatalf("got %v, want a %s panic", err, PType)
	}

	// empty paths: rejected before touching the filesystem
	if _, err := th.httpTLSConfig(tlsValue(t, th, "", "")); err == nil {
		t.Fatal("want a panic for empty cert/key")
	} else if p, ok := err.(*Panic); !ok || p.Code != PArgs {
		t.Fatalf("got %v, want a %s panic", err, PArgs)
	}

	// missing files: a clean, recoverable E_TLS failure
	v := tlsValue(t, th, filepath.Join(dir, "no.pem"), filepath.Join(dir, "no.key"))
	if _, err := th.httpTLSConfig(v); err == nil {
		t.Fatal("want an E_TLS failure for missing files")
	} else if f, ok := err.(*Failure); !ok || f.Code() != "E_TLS" {
		t.Fatalf("got %v, want an E_TLS failure", err)
	}

	// a malformed PEM file
	if err := os.WriteFile(cert, []byte("not a certificate"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := th.httpTLSConfig(tlsValue(t, th, cert, key)); err == nil {
		t.Fatal("want an E_TLS failure for a malformed certificate")
	} else if f, ok := err.(*Failure); !ok || f.Code() != "E_TLS" {
		t.Fatalf("got %v, want an E_TLS failure", err)
	}
	writeSelfSigned(t, cert, key, 1, time.Now())

	// a certificate and key that do not belong together
	other, otherKey := filepath.Join(dir, "o.pem"), filepath.Join(dir, "o.key")
	writeSelfSigned(t, other, otherKey, 2, time.Now())
	if _, err := th.httpTLSConfig(tlsValue(t, th, cert, otherKey)); err == nil {
		t.Fatal("want an E_TLS failure for a mismatched pair")
	} else if f, ok := err.(*Failure); !ok || f.Code() != "E_TLS" {
		t.Fatalf("got %v, want an E_TLS failure", err)
	}

	cfg, err := th.httpTLSConfig(tlsValue(t, th, cert, key))
	if err != nil {
		t.Fatalf("httpTLSConfig: %v", err)
	}
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Fatalf("MinVersion %d, want TLS 1.2", cfg.MinVersion)
	}
	if cfg.GetCertificate == nil {
		t.Fatal("GetCertificate is not set")
	}
	if _, err := cfg.GetCertificate(nil); err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
}

func TestServeTLSAndReload(t *testing.T) {
	th := tlsThread(t)
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "c.pem"), filepath.Join(dir, "c.key")
	pool1 := writeSelfSigned(t, cert, key, 1, time.Now().Add(time.Second))
	cfg, err := th.httpTLSConfig(tlsValue(t, th, cert, key))
	if err != nil {
		t.Fatal(err)
	}
	url := startTLS(t, cfg, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "ok")
	}))

	body, serial, _ := httpsGet(t, pool1, url)
	if body != "ok" || serial != 1 {
		t.Fatalf("got body %q serial %d, want \"ok\" 1", body, serial)
	}

	// replace the files on disk; the same server must serve the new cert
	pool2 := writeSelfSigned(t, cert, key, 2, time.Now().Add(2*time.Second))
	body, serial, _ = httpsGet(t, pool2, url)
	if body != "ok" || serial != 2 {
		t.Fatalf("after renewal: body %q serial %d, want \"ok\" 2", body, serial)
	}
}

func TestServeTLSKeepsServingOnBrokenRenewal(t *testing.T) {
	th := tlsThread(t)
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "c.pem"), filepath.Join(dir, "c.key")
	pool := writeSelfSigned(t, cert, key, 1, time.Now().Add(time.Second))
	cfg, err := th.httpTLSConfig(tlsValue(t, th, cert, key))
	if err != nil {
		t.Fatal(err)
	}
	url := startTLS(t, cfg, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "ok")
	}))

	if _, serial, _ := httpsGet(t, pool, url); serial != 1 {
		t.Fatalf("initial serial %d, want 1", serial)
	}
	// a half-finished renewal (garbage cert) must not take the server down
	if err := os.WriteFile(cert, []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	bump := time.Now().Add(3 * time.Second)
	if err := os.Chtimes(cert, bump, bump); err != nil {
		t.Fatal(err)
	}
	if body, serial, _ := httpsGet(t, pool, url); body != "ok" || serial != 1 {
		t.Fatalf("after broken renewal: body %q serial %d, want \"ok\" 1", body, serial)
	}
}

func TestServeTLSErrorProgram(t *testing.T) {
	src := `
import "http"

fn h(req: http.Request) -> http.Response => http.text(200, body: "ok")

fn code() -> Str uses net {
    http.serve("127.0.0.1:0", handler: h, tls: http.Tls{cert: "missing.pem", key: "missing.pem"}) catch e { return e.code }
    return "served"
}

fn main() -> ! uses net {
    print(code())
}
`
	out, res := runSrc(t, src, "net")
	if res.Status != "ok" {
		t.Fatalf("status %s: %s\noutput: %s", res.Status, res.Describe(), out)
	}
	if out != "E_TLS\n" {
		t.Fatalf("got %q, want a clean E_TLS failure", out)
	}
}

func TestServeTLSVersionFloor(t *testing.T) {
	th := tlsThread(t)
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "c.pem"), filepath.Join(dir, "c.key")
	pool := writeSelfSigned(t, cert, key, 1, time.Now())
	cfg, err := th.httpTLSConfig(tlsValue(t, th, cert, key))
	if err != nil {
		t.Fatal(err)
	}
	url := startTLS(t, cfg, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "ok")
	}))
	// a client capped at TLS 1.1 must be refused
	tr := &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs: pool, ServerName: "localhost",
		MinVersion: tls.VersionTLS10, MaxVersion: tls.VersionTLS11,
	}}
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	if _, err := client.Get(url); err == nil {
		t.Fatal("TLS 1.1 must be refused by the server")
	}
}
