package interp

// Web serving: the http.serve handler, routing (http.dispatch), static
// files, forms, redirects and cookies.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/marcodc74/pygo/internal/ast"
)

// field returns a struct field by name.
func field(s *Struct, name string) Value {
	return s.Snapshot()[s.T.FieldIdx[name]]
}

// httpResponse builds an http.Response (no cookies).
func (th *Thread) httpResponse(status int64, body string, headers *Map) *Struct {
	st := th.in.stdModule("http").Types["Response"].(*StructType)
	return &Struct{T: st, F: []Value{status, body, headers, NewList(nil)}}
}

func (th *Thread) httpText(status int64, body string) *Struct {
	h := NewMap()
	h.Set("content-type", "text/plain; charset=utf-8")
	return th.httpResponse(status, body, h)
}

// httpHandler adapts a Pygo handler fn(Request) -> Response to net/http.
// Each request runs on its own thread; failures and panics are logged as
// JSON lines and answered with 500. Bodies over maxBody bytes get 413
// without calling the handler.
func (th *Thread) httpHandler(handler Value, maxBody int64) http.Handler {
	in := th.in
	mod := th.mod()
	httpMod := in.stdModule("http")
	reqType := httpMod.Types["Request"].(*StructType)
	respType := httpMod.Types["Response"].(*StructType)
	logErr := func(msg string, extra map[string]string) {
		var b bytes.Buffer
		b.WriteString(`{"ts":`)
		encodeJSON(&b, time.Now().UTC().Format(time.RFC3339Nano), 0, 0)
		b.WriteString(`,"level":"error","msg":`)
		encodeJSON(&b, msg, 0, 0)
		keys := make([]string, 0, len(extra))
		for k := range extra {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			b.WriteString(`,`)
			encodeJSON(&b, k, 0, 0)
			b.WriteString(`:`)
			encodeJSON(&b, extra[k], 0, 0)
		}
		b.WriteString("}\n")
		in.errw.WriteString(b.String())
		in.errw.Flush()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > maxBody {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
		if err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			} else {
				http.Error(w, "cannot read request body", http.StatusBadRequest)
			}
			return
		}
		query := r.URL.Query()
		q := NewMap()
		for _, k := range sortedKeys(query) {
			q.Set(k, query.Get(k))
		}
		hd := NewMap()
		for _, k := range sortedKeys(r.Header) {
			hd.Set(strings.ToLower(k), r.Header.Get(k))
		}
		ck := NewMap()
		for _, c := range r.Cookies() {
			if _, seen := ck.Get(c.Name); !seen {
				ck.Set(c.Name, c.Value)
			}
		}
		req := &Struct{T: reqType, F: []Value{r.Method, r.URL.Path, q, hd, string(body), NewMap(), ck}}
		nth := in.newThread(mod)
		var res Value
		func() {
			defer func() {
				if p := recover(); p != nil {
					err = &Panic{Code: PInternal, Message: fmt.Sprint(p)}
				}
			}()
			defer nth.flushSteps()
			res, err = nth.callValue(handler, []Value{req}, nil, ast.Pos{})
		}()
		if err != nil {
			logErr("handler error", map[string]string{"error": err.Error(), "path": r.URL.Path})
			http.Error(w, "internal error", 500)
			return
		}
		resp, ok := res.(*Struct)
		if !ok || resp.T != respType {
			logErr("handler must return http.Response", map[string]string{"got": TypeName(res)})
			http.Error(w, "internal error", 500)
			return
		}
		f := resp.Snapshot()
		var cookies []*http.Cookie
		if cl, ok := f[3].(*List); ok {
			for _, v := range cl.Snapshot() {
				c, err := toCookie(v)
				if err != nil {
					logErr("invalid cookie", map[string]string{"error": err.Error(), "path": r.URL.Path})
					http.Error(w, "internal error", 500)
					return
				}
				cookies = append(cookies, c)
			}
		}
		if hm, ok := f[2].(*Map); ok {
			ks, vs := hm.Items()
			for i := range ks {
				w.Header().Set(Str(ks[i]), Str(vs[i]))
			}
		}
		for _, c := range cookies {
			http.SetCookie(w, c)
		}
		status, _ := f[0].(int64)
		if status < 100 || status > 999 {
			status = 500
		}
		w.WriteHeader(int(status))
		io.WriteString(w, Str(f[1]))
	})
}

// toCookie converts an http.Cookie struct value.
func toCookie(v Value) (*http.Cookie, error) {
	s, ok := v.(*Struct)
	if !ok || s.T.Name != "Cookie" {
		return nil, fmt.Errorf("cookies must be http.Cookie values, got %s", TypeName(v))
	}
	c := &http.Cookie{
		Name:     Str(field(s, "name")),
		Value:    Str(field(s, "value")),
		Path:     Str(field(s, "path")),
		HttpOnly: field(s, "http_only") == true,
		Secure:   field(s, "secure") == true,
	}
	switch n, _ := field(s, "max_age").(int64); {
	case n > 0:
		c.MaxAge = int(n)
	case n < 0:
		c.MaxAge = -1 // delete now
	}
	switch Str(field(s, "same_site")) {
	case "Lax":
		c.SameSite = http.SameSiteLaxMode
	case "Strict":
		c.SameSite = http.SameSiteStrictMode
	case "None":
		c.SameSite = http.SameSiteNoneMode
		if !c.Secure {
			return nil, fmt.Errorf("cookie %q: same_site \"None\" requires secure: true", c.Name)
		}
	default:
		return nil, fmt.Errorf("cookie %q: same_site must be \"Lax\", \"Strict\" or \"None\"", c.Name)
	}
	if err := c.Valid(); err != nil {
		return nil, err
	}
	return c, nil
}

// ---------- routing ----------

var routeMethods = map[string]bool{"GET": true, "HEAD": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true, "OPTIONS": true}

// routePat is a compiled route path: literal segments, {name} (one
// non-empty segment) and a final {name...} (the rest of the path).
type routePat struct {
	method string
	segs   []string // literal text, or "" for a {name} segment
	names  []string // parameter name of each segment ("" for literals)
	rest   string   // name of a final {name...}, "" if none
}

var routeCache sync.Map // "METHOD path" -> *routePat

func compileRoute(method, path string) (*routePat, error) {
	key := method + " " + path
	if p, ok := routeCache.Load(key); ok {
		return p.(*routePat), nil
	}
	if !routeMethods[method] {
		return nil, fmt.Errorf("invalid route method %q: use GET, HEAD, POST, PUT, PATCH, DELETE or OPTIONS", method)
	}
	if !strings.HasPrefix(path, "/") {
		return nil, fmt.Errorf("route path %q must start with /", path)
	}
	p := &routePat{method: method}
	seen := map[string]bool{}
	parts := strings.Split(path[1:], "/")
	for i, seg := range parts {
		if !strings.ContainsAny(seg, "{}") {
			p.segs = append(p.segs, seg)
			p.names = append(p.names, "")
			continue
		}
		if !strings.HasPrefix(seg, "{") || !strings.HasSuffix(seg, "}") || strings.Count(seg, "{") != 1 || strings.Count(seg, "}") != 1 {
			return nil, fmt.Errorf("route path %q: a segment is either text or a whole {name}", path)
		}
		name := seg[1 : len(seg)-1]
		rest := strings.HasSuffix(name, "...")
		name = strings.TrimSuffix(name, "...")
		if !isIdent(name) || seen[name] {
			return nil, fmt.Errorf("route path %q: invalid or repeated parameter name %q", path, name)
		}
		seen[name] = true
		if rest {
			if i != len(parts)-1 {
				return nil, fmt.Errorf("route path %q: {%s...} must be the last segment", path, name)
			}
			p.rest = name
			break
		}
		p.segs = append(p.segs, "")
		p.names = append(p.names, name)
	}
	routeCache.Store(key, p)
	return p, nil
}

func isIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || i > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// match returns the parameters if path matches the pattern.
func (p *routePat) match(path string) (map[string]string, bool) {
	if !strings.HasPrefix(path, "/") {
		return nil, false
	}
	parts := strings.Split(path[1:], "/")
	if p.rest == "" && len(parts) != len(p.segs) || p.rest != "" && len(parts) <= len(p.segs) {
		return nil, false
	}
	params := map[string]string{}
	for i, seg := range p.segs {
		if p.names[i] != "" {
			if parts[i] == "" {
				return nil, false
			}
			params[p.names[i]] = parts[i]
		} else if parts[i] != seg {
			return nil, false
		}
	}
	if p.rest != "" {
		params[p.rest] = strings.Join(parts[len(p.segs):], "/")
	}
	return params, true
}

// httpDispatch calls the handler of the first route matching the request
// method and path; 405 when only the method differs, 404 otherwise.
func (th *Thread) httpDispatch(req *Struct, routes *List) (Value, error) {
	method, _ := field(req, "method").(string)
	path, _ := field(req, "path").(string)
	var allowed []string
	for _, rv := range routes.Snapshot() {
		route, ok := rv.(*Struct)
		if !ok {
			return nil, perr(PArgs, "", "http.dispatch: routes must be http.Route values, got %s", TypeName(rv))
		}
		rm, _ := field(route, "method").(string)
		rp, _ := field(route, "path").(string)
		pat, err := compileRoute(rm, rp)
		if err != nil {
			return nil, perr(PArgs, `write http.Route{method: "GET", path: "/items/{id}", handler: f}`, "http.dispatch: %v", err)
		}
		params, ok := pat.match(path)
		if !ok {
			continue
		}
		if rm != method && !(rm == "GET" && method == "HEAD") {
			allowed = append(allowed, rm)
			if rm == "GET" {
				allowed = append(allowed, "HEAD")
			}
			continue
		}
		pm := NewMap()
		for _, k := range sortedKeys(params) {
			pm.Set(k, params[k])
		}
		f := req.Snapshot()
		f[req.T.FieldIdx["params"]] = pm
		return th.call(field(route, "handler"), &Struct{T: req.T, F: f})
	}
	if len(allowed) > 0 {
		r := th.httpText(405, "method not allowed")
		r.F[2].(*Map).Set("allow", strings.Join(dedup(allowed), ", "))
		return r, nil
	}
	return th.httpText(404, "not found"), nil
}

func dedup(xs []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// ---------- static files, forms, redirects ----------

// contentTypes is fixed (mime.TypeByExtension reads the OS registry on
// Windows, which would make responses differ between systems).
var contentTypes = map[string]string{
	".html": "text/html; charset=utf-8", ".htm": "text/html; charset=utf-8",
	".css": "text/css; charset=utf-8", ".js": "text/javascript; charset=utf-8",
	".mjs": "text/javascript; charset=utf-8", ".json": "application/json",
	".map": "application/json", ".txt": "text/plain; charset=utf-8",
	".md": "text/markdown; charset=utf-8", ".csv": "text/csv; charset=utf-8",
	".xml": "application/xml", ".svg": "image/svg+xml", ".png": "image/png",
	".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif",
	".webp": "image/webp", ".avif": "image/avif", ".ico": "image/x-icon",
	".wasm": "application/wasm", ".pdf": "application/pdf",
	".woff": "font/woff", ".woff2": "font/woff2", ".ttf": "font/ttf", ".otf": "font/otf",
	".mp3": "audio/mpeg", ".ogg": "audio/ogg", ".wav": "audio/wav",
	".mp4": "video/mp4", ".webm": "video/webm",
}

// httpStatic serves the file named by the route parameter "path" (a final
// {path...}) from dir. Paths with "..", hidden segments (".env", ".git"),
// backslashes, or symlinks leading outside dir are not served.
func (th *Thread) httpStatic(req *Struct, dir string) (Value, error) {
	params, _ := field(req, "params").(*Map)
	var rel Value
	ok := false
	if params != nil {
		rel, ok = params.Get("path")
	}
	if !ok {
		return nil, perr(PArgs, `route it as http.Route{method: "GET", path: "/static/{path...}", handler: fn(req) => http.static(req, dir: "public")}`,
			"http.static needs a route whose path ends with {path...}")
	}
	name := Str(rel)
	notFound := th.httpText(404, "not found")
	if strings.ContainsAny(name, "\\\x00") {
		return notFound, nil
	}
	for _, seg := range strings.Split(name, "/") {
		if strings.HasPrefix(seg, ".") {
			return notFound, nil
		}
	}
	full := filepath.Join(dir, filepath.FromSlash(name))
	if st, err := os.Stat(full); err == nil && st.IsDir() {
		full = filepath.Join(full, "index.html")
	}
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return notFound, nil
	}
	real, err := filepath.EvalSymlinks(full)
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return th.httpText(403, "forbidden"), nil
		}
		return notFound, nil
	}
	if r, err := filepath.Rel(root, real); err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) || filepath.IsAbs(r) {
		return notFound, nil
	}
	data, err := os.ReadFile(real)
	switch {
	case errors.Is(err, fs.ErrPermission):
		return th.httpText(403, "forbidden"), nil
	case err != nil:
		return notFound, nil
	}
	ct, ok := contentTypes[strings.ToLower(filepath.Ext(real))]
	if !ok {
		ct = "application/octet-stream"
	}
	h := NewMap()
	h.Set("content-type", ct)
	h.Set("x-content-type-options", "nosniff")
	return th.httpResponse(200, string(data), h), nil
}

// httpForm decodes an application/x-www-form-urlencoded body (first value
// of each field, fields in name order).
func (th *Thread) httpForm(req *Struct) (Value, error) {
	ct := ""
	if hd, ok := field(req, "headers").(*Map); ok {
		if v, ok := hd.Get("content-type"); ok {
			ct = strings.ToLower(Str(v))
		}
	}
	if ct != "" && !strings.HasPrefix(ct, "application/x-www-form-urlencoded") {
		return nil, th.fail("E_FORM", "expected a form body (application/x-www-form-urlencoded), got %s", ct)
	}
	vals, err := url.ParseQuery(Str(field(req, "body")))
	if err != nil {
		return nil, th.fail("E_FORM", "invalid form body: %v", err)
	}
	m := NewMap()
	for _, k := range sortedKeys(vals) {
		m.Set(k, vals.Get(k))
	}
	return m, nil
}

func (th *Thread) httpRedirect(location string, status int64) (Value, error) {
	switch status {
	case 301, 302, 303, 307, 308:
	default:
		return nil, perr(PArgs, "use 303 after a form POST, 301/308 for moved pages, 302/307 for temporary ones", "http.redirect: status %d is not a redirect", status)
	}
	if strings.ContainsAny(location, "\r\n") {
		return nil, perr(PArgs, "", "http.redirect: location contains a line break")
	}
	h := NewMap()
	h.Set("location", location)
	return th.httpResponse(status, "", h), nil
}
