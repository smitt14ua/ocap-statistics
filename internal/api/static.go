package api

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
)

// Content-Security-Policy of the web pages. Scripts are never inline; styles may be (the
// pages build report screenshots with style attributes).
const (
	cspSite = "default-src 'self'; script-src 'self' https://cdnjs.cloudflare.com; style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data: blob: https:; connect-src 'self'; font-src 'self'; object-src 'none'; base-uri 'self'; form-action 'self'"
	cspAdmin = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: https:; " +
		"connect-src 'self'; font-src 'self'; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'"
	cspDocs = "default-src 'self'; script-src 'self' https://cdn.jsdelivr.net; style-src 'self' 'unsafe-inline' https://cdn.jsdelivr.net; " +
		"img-src 'self' data: https:; connect-src 'self'; object-src 'none'; base-uri 'self'; frame-ancestors 'none'"
)

// staticFile is an embedded file prepared for serving.
type staticFile struct {
	body        []byte
	gzipped     []byte
	contentType string
	etag        string
}

// staticHandler serves the embedded web UI from memory with ETags and gzip. Files are not
// fingerprinted, so browsers revalidate them (a cheap 304) on every load.
type staticHandler struct {
	files map[string]*staticFile
}

func newStaticHandler(fsys fs.FS) (*staticHandler, error) {
	h := &staticHandler{files: map[string]*staticFile{}}
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		ct := mime.TypeByExtension(path.Ext(p))
		if ct == "" {
			ct = http.DetectContentType(body)
		}
		sum := sha256.Sum256(body)
		f := &staticFile{body: body, contentType: ct, etag: `"` + base64.RawURLEncoding.EncodeToString(sum[:12]) + `"`}
		if compressible(ct) && len(body) >= minGzipSize {
			var buf bytes.Buffer
			zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
			_, _ = zw.Write(body)
			if zw.Close() == nil && buf.Len() < len(body) {
				f.gzipped = buf.Bytes()
			}
		}
		h.files["/"+p] = f
		return nil
	})
	return h, err
}

func compressible(ct string) bool {
	return strings.HasPrefix(ct, "text/") || strings.Contains(ct, "javascript") ||
		strings.Contains(ct, "json") || strings.Contains(ct, "svg")
}

func (h *staticHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p := path.Clean("/" + r.URL.Path)
	if strings.HasSuffix(r.URL.Path, "/") && p != "/" {
		p += "/"
	}
	if strings.HasSuffix(p, "/") {
		p += "index.html"
	}
	f, ok := h.files[p]
	if !ok {
		// "/admin" → "/admin/"
		if _, isDir := h.files[p+"/index.html"]; isDir {
			http.Redirect(w, r, p+"/", http.StatusMovedPermanently)
			return
		}
		http.NotFound(w, r)
		return
	}

	hdr := w.Header()
	hdr.Set("Content-Type", f.contentType)
	hdr.Set("ETag", f.etag)
	hdr.Set("Cache-Control", "no-cache")
	hdr.Add("Vary", "Accept-Encoding")
	if strings.HasSuffix(p, ".html") {
		if strings.HasPrefix(p, "/admin/") {
			hdr.Set("Content-Security-Policy", cspAdmin)
			hdr.Set("X-Frame-Options", "DENY")
			hdr.Set("X-Robots-Tag", "noindex")
		} else {
			hdr.Set("Content-Security-Policy", cspSite)
		}
	}
	if etagMatches(r.Header.Get("If-None-Match"), f.etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	writeBody(w, r, f.body, f.gzipped)
}
