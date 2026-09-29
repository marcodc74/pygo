package interp

// TLS support for http.serve, with certificate reload without a restart.

import (
	"crypto/tls"
	"os"
	"sync"
	"time"
)

// certReloader returns a tls.Config callback that re-reads the certificate
// when either file's modification time or size changes. A pair that fails to
// load (missing, malformed, mismatched) keeps serving the last good
// certificate, so a broken renewal cannot take the server down.
type certReloader struct {
	certFile, keyFile string

	mu                sync.Mutex
	cert              *tls.Certificate
	certMod, keyMod   time.Time
	certSize, keySize int64
}

// newCertReloader loads the pair once, so a bad path or a bad PEM fails
// before the server binds the port.
func newCertReloader(certFile, keyFile string) (*certReloader, error) {
	r := &certReloader{certFile: certFile, keyFile: keyFile}
	if err := r.reload(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *certReloader) reload() error {
	ci, err := os.Stat(r.certFile)
	if err != nil {
		return err
	}
	ki, err := os.Stat(r.keyFile)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cert != nil && ci.ModTime().Equal(r.certMod) && ki.ModTime().Equal(r.keyMod) &&
		ci.Size() == r.certSize && ki.Size() == r.keySize {
		return nil
	}
	c, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		return err
	}
	r.cert, r.certMod, r.keyMod = &c, ci.ModTime(), ki.ModTime()
	r.certSize, r.keySize = ci.Size(), ki.Size()
	return nil
}

// GetCertificate is the tls.Config callback. It never fails while a previous
// certificate is available.
func (r *certReloader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	if err := r.reload(); err != nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.cert != nil {
			return r.cert, nil
		}
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cert, nil
}

// httpTLSConfig validates the http.Tls argument and builds the TLS config.
// TLS 1.2 is the floor; older clients are refused.
func (th *Thread) httpTLSConfig(v Value) (*tls.Config, error) {
	want, _ := th.in.stdModule("http").Types["Tls"].(*StructType)
	s, ok := v.(*Struct)
	if !ok || want == nil || s.T != want {
		return nil, perr(PType, `pass tls: http.Tls{cert: "cert.pem", key: "key.pem"}`,
			"http.serve: tls must be http.Tls, got %s", TypeName(v))
	}
	cert := Str(field(s, "cert"))
	key := Str(field(s, "key"))
	if cert == "" || key == "" {
		return nil, perr(PArgs, `pass tls: http.Tls{cert: "cert.pem", key: "key.pem"}`,
			"http.serve: tls.cert and tls.key must be certificate file paths")
	}
	r, err := newCertReloader(cert, key)
	if err != nil {
		return nil, th.fail("E_TLS", "http.serve: cannot load TLS certificate: %v", err)
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: r.GetCertificate}, nil
}
