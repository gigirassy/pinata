// main.go
package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"html"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// --------------------------------------------------------------------
// Configuration: HTTP client and buffer pool (memory tuned)
// --------------------------------------------------------------------
var httpClient = &http.Client{
	Timeout: 15 * time.Second,
	Transport: &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   8 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:        6,
		MaxIdleConnsPerHost: 3,
		IdleConnTimeout:     60 * time.Second,
		TLSHandshakeTimeout: 8 * time.Second,
	},
}

var copyBufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 32*1024) // 32KB buffer reused
		return &b
	},
}

const pinterestSearchURL = "https://www.pinterest.com/resource/BaseSearchResource/get/"
const cookieName = "pinata_bm"

// --------------------------------------------------------------------
// Bookmarks: types, key, limits
// --------------------------------------------------------------------
type BookmarkEntry struct {
	Type  string `json:"type"`  // "q" or "img"
	Value string `json:"value"` // query or image URL
}

var bookmarkKey []byte
var bookmarkingEnabled bool
var disableReverse bool

const maxBookmarks = 30
const maxItemLen = 256

// --------------------------------------------------------------------
// init: read env variables
// - PINATA_BOOKMARK_KEY: base64 32-byte key to enable bookmarks
// - PINATA_DISABLE_REVERSE: "1"/"true"/"yes" disables reverse search link
// --------------------------------------------------------------------
func init() {
	// PINATA_BOOKMARK_KEY
	if kb := os.Getenv("PINATA_BOOKMARK_KEY"); kb != "" {
		if decoded, err := base64.StdEncoding.DecodeString(kb); err == nil && len(decoded) == 32 {
			bookmarkKey = decoded
			bookmarkingEnabled = true
			log.Println("Bookmarking enabled")
		} else {
			bookmarkingEnabled = false
			log.Println("PINATA_BOOKMARK_KEY provided but invalid; bookmarking disabled")
		}
	} else {
		bookmarkingEnabled = false
		log.Println("PINATA_BOOKMARK_KEY not set; bookmarking disabled")
	}

	// PINATA_DISABLE_REVERSE
	switch strings.ToLower(strings.TrimSpace(os.Getenv("PINATA_DISABLE_REVERSE"))) {
	case "1", "true", "yes":
		disableReverse = true
		log.Println("Reverse image search disabled via PINATA_DISABLE_REVERSE")
	default:
		disableReverse = false
	}
}

// --------------------------------------------------------------------
// Encryption helpers for cookie storage (AES-GCM)
// --------------------------------------------------------------------
func encryptBookmarks(entries []BookmarkEntry) (string, error) {
	if !bookmarkingEnabled {
		return "", nil
	}
	plain, err := json.Marshal(entries)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(bookmarkKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := gcm.Seal(nonce, nonce, plain, nil)
	return base64.RawURLEncoding.EncodeToString(ct), nil
}

func decryptBookmarks(encoded string) ([]BookmarkEntry, error) {
	if !bookmarkingEnabled {
		return nil, nil
	}
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(bookmarkKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	ns := gcm.NonceSize()
	if len(data) < ns {
		return nil, io.ErrUnexpectedEOF
	}
	nonce := data[:ns]
	ct := data[ns:]
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, err
	}
	// Try new format ([]BookmarkEntry)
	var entries []BookmarkEntry
	if err := json.Unmarshal(plain, &entries); err == nil {
		return entries, nil
	}
	// Fallback to legacy []string -> convert to type "q"
	var arr []string
	if err := json.Unmarshal(plain, &arr); err == nil {
		out := make([]BookmarkEntry, 0, len(arr))
		for _, s := range arr {
			out = append(out, BookmarkEntry{Type: "q", Value: s})
		}
		return out, nil
	}
	return nil, io.ErrUnexpectedEOF
}

// --------------------------------------------------------------------
// Cookie helpers: read, set, clear
// --------------------------------------------------------------------
func readBookmarksFromReq(r *http.Request) []BookmarkEntry {
	if !bookmarkingEnabled {
		return nil
	}
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" {
		return nil
	}
	entries, err := decryptBookmarks(c.Value)
	if err != nil {
		// invalid cookie -> ignore
		return nil
	}
	return entries
}

func setBookmarksCookie(w http.ResponseWriter, entries []BookmarkEntry) {
	if !bookmarkingEnabled {
		return
	}
	seen := map[string]bool{}
	out := make([]BookmarkEntry, 0, len(entries))
	for _, e := range entries {
		v := strings.TrimSpace(e.Value)
		if v == "" {
			continue
		}
		if len(v) > maxItemLen {
			v = v[:maxItemLen]
		}
		key := e.Type + "|" + v
		if seen[key] {
			continue
		}
		seen[key] = true
		if e.Type != "q" && e.Type != "img" {
			e.Type = "q"
		}
		out = append(out, BookmarkEntry{Type: e.Type, Value: v})
		if len(out) >= maxBookmarks {
			break
		}
	}
	enc, err := encryptBookmarks(out)
	if err != nil {
		// fail silently
		return
	}
	c := &http.Cookie{
		Name:     cookieName,
		Value:    enc,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		// Secure: true, // enable when serving over HTTPS
		MaxAge: 60 * 60 * 24 * 365 * 10,
	}
	http.SetCookie(w, c)
}

func clearBookmarksCookie(w http.ResponseWriter) {
	c := &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	}
	http.SetCookie(w, c)
}

// --------------------------------------------------------------------
// Stylesheet and minimal HTML templates (no JS)
// --------------------------------------------------------------------
const cssContent = `
:root{--bg:#0b0f17;--muted:#94a3b8;--text:#e6e6ff;--accent:#7c3aed;--card-shadow:rgba(0,0,0,0.6)}
*{box-sizing:border-box}html,body{height:100%}body{margin:0;padding:20px;background:linear-gradient(180deg,#071020 0%,#0b0f17 100%);color:var(--text);font-family:ui-monospace,Menlo,Monaco,monospace}
a{color:inherit}
.header{display:flex;gap:12px;align-items:center;margin-bottom:18px;flex-wrap:wrap}
.brand{font-size:20px;font-weight:700;color:var(--accent);text-decoration:none}
.search-box{margin-left:auto;display:flex;gap:8px;align-items:center;flex:0 1 auto}
.search-block{width:100%;display:flex;gap:8px;margin-top:14px}
.search-inline{display:flex;gap:8px;align-items:center;min-width:0}
input[type="text"]{background:transparent;border:1px solid rgba(255,255,255,0.06);padding:8px 12px;color:var(--text);min-width:120px;border-radius:8px;outline:none}
button[type="submit"],.btn-save{background:linear-gradient(90deg,var(--accent),#5b21b6);color:white;border:none;padding:8px 12px;border-radius:8px;cursor:pointer}
.btn-save{font-weight:600}
.img-container { column-width: 260px; column-gap: 16px; width: 100%; max-width: 1400px; margin-top: 18px; }
.card { display:inline-block; width:100%; margin:0 0 16px; border-radius:10px; overflow:hidden; background:linear-gradient(180deg,rgba(255,255,255,0.01),rgba(255,255,255,0.02)); box-shadow:0 6px 18px rgba(3,7,18,0.6); border:1px solid rgba(124,58,237,0.06); break-inside: avoid; -webkit-column-break-inside: avoid; -moz-column-break-inside: avoid; min-height:0; position:relative; }
.card img { display:block; width:100%; height:auto; object-fit:cover; background:#08101a; }
.card-controls { position:absolute; top:8px; right:8px; display:flex; gap:8px; align-items:center; }
.btn-save-mini { background: rgba(124,58,237,0.12); color: var(--text); border: none; padding:6px; border-radius:999px; cursor:pointer; font-weight:700; }
.magnifier{background:rgba(0,0,0,0.45);border:1px solid rgba(255,255,255,0.06);color:var(--text);padding:6px;border-radius:999px;font-size:14px;width:34px;height:34px;display:inline-flex;align-items:center;justify-content:center;text-decoration:none}
.bookmarks{margin-left:12px;color:var(--muted);font-size:14px}
.bookmark-list{margin-top:10px;display:flex;gap:8px;flex-wrap:wrap}
.bookmark-pill{background:rgba(255,255,255,0.03);padding:6px 8px;border-radius:999px;border:1px solid rgba(255,255,255,0.04);font-size:13px;display:flex;gap:6px;align-items:center}
.bookmark-pill form{display:inline}
.bookmark-remove-btn{background:transparent;border:none;color:#ff7b7b;font-weight:700;cursor:pointer;padding:0 6px}
.export-form{margin-top:12px;display:flex;gap:8px;align-items:center}
.pagination{text-align:center;margin:26px 0}
.pagination a{color:var(--accent);text-decoration:none;padding:8px 12px;border-radius:8px;border:1px solid rgba(124,58,237,0.12);background:rgba(124,58,237,0.02)}
.footer-note{color:var(--muted);font-size:12px;margin-top:22px}
@media (max-width:640px){ body{padding:12px;font-size:18px} .brand{font-size:22px} input[type="text"]{min-width:120px;padding:12px 14px;font-size:16px} button[type="submit"],.btn-save{padding:10px 14px;font-size:16px;border-radius:10px} .img-container{column-width:180px;column-gap:12px} .search-block{gap:10px;flex-direction:column} .search-inline{width:100%} .search-box{margin-left:0;width:100%} .bookmarks{order:3;width:100%;margin-top:8px} }
`

<<<<<<< HEAD
// Static index page (no JS). Bookmarks rendered server-side only here.
func indexHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf8")
	// header and intro
	io.WriteString(w, `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Pinata - Search</title><link rel="stylesheet" href="/static/style.css"></head><body>`)
	io.WriteString(w, `<div class="header"><a class="brand" href="/">Pinata</a><div class="search-box"></div></div>`)
	io.WriteString(w, `<div style="color:#94a3b8; margin-bottom:12px;">Search images from Pinterest — submit a search to view results.</div>`)
	// search block
	io.WriteString(w, `<form class="search-block" method="get" action="/search"><input type="text" name="q" placeholder="Search Image" required maxlength="64"><button type="submit">Search</button></form>`)

	// bookmarks area (server-rendered)
	if bookmarkingEnabled {
		items := readBookmarksFromReq(r)
		io.WriteString(w, `<div class="bookmarks"><div style="font-size:14px;color:var(--muted);margin-top:8px">Saved searches</div><div class="bookmark-list">`)
		for _, q := range items {
			escaped := html.EscapeString(q)
			// Each bookmark pill: link to /search?q=... and a small form to remove
			io.WriteString(w, `<span class="bookmark-pill"><a href="/search?q=`+url.QueryEscape(q)+`">`+escaped+`</a>`)
			// remove form
			io.WriteString(w, `<form method="post" action="/bookmark_remove" style="display:inline;margin:0 0 0 6px;"><input type="hidden" name="q" value="`+html.EscapeString(q)+`"><button class="bookmark-remove-btn" type="submit" title="Remove">✕</button></form></span>`)
		}
		io.WriteString(w, `</div></div>`)
	}

	io.WriteString(w, `<div class="footer-note">Powered by Pinata • Reverse search via Tineye</div></body></html>`)
=======
// --------------------------------------------------------------------
// Handlers
// --------------------------------------------------------------------
func styleHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	_, _ = io.WriteString(w, cssContent)
>>>>>>> cc7ebb9 (image bookmarking, bookmarks export/import)
}

// Index (front page) - server-rendered bookmarks and import/export forms
func indexHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// header and search block
	_, _ = io.WriteString(w, `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Pinata - Search</title><link rel="stylesheet" href="/static/style.css"></head><body>`)
	_, _ = io.WriteString(w, `<div class="header"><a class="brand" href="/">Pinata</a><div class="search-box"></div></div>`)
	_, _ = io.WriteString(w, `<div style="color:#94a3b8; margin-bottom:12px;">Search images from Pinterest — submit a search to view results.</div>`)
	_, _ = io.WriteString(w, `<form class="search-block" method="get" action="/search"><input type="text" name="q" placeholder="Search Image" required maxlength="64"><button type="submit">Search</button></form>`)

	// bookmarks shown only on index
	if bookmarkingEnabled {
		items := readBookmarksFromReq(r)
		_, _ = io.WriteString(w, `<div class="bookmarks"><div style="font-size:14px;color:var(--muted);margin-top:8px">Saved bookmarks</div><div class="bookmark-list">`)
		for _, e := range items {
			escaped := html.EscapeString(e.Value)
			if e.Type == "q" {
				_, _ = io.WriteString(w, `<span class="bookmark-pill"><a href="/search?q=`+url.QueryEscape(e.Value)+`">`+escaped+`</a>`)
			} else {
				_, _ = io.WriteString(w, `<span class="bookmark-pill"><a href="/image_proxy?url=`+url.QueryEscape(e.Value)+`">`+escaped+`</a>`)
			}
			_, _ = io.WriteString(w, `<form method="post" action="/bookmark_remove" style="display:inline;margin:0 0 0 6px;"><input type="hidden" name="type" value="`+html.EscapeString(e.Type)+`"><input type="hidden" name="value" value="`+html.EscapeString(e.Value)+`"><button class="bookmark-remove-btn" type="submit" title="Remove">✕</button></form></span>`)
		}
		_, _ = io.WriteString(w, `</div>`)

		// export and import
		_, _ = io.WriteString(w, `<div class="export-form"><form method="get" action="/bookmarks/export"><button type="submit" class="btn-save">Export JSON</button></form>`)
		_, _ = io.WriteString(w, `<form method="post" action="/bookmarks/import" enctype="multipart/form-data" style="margin-left:8px;"><input type="file" name="file" accept="application/json" required><button type="submit" class="btn-save" style="margin-left:8px">Import JSON</button></form></div>`)
		_, _ = io.WriteString(w, `</div>`)
	}

	_, _ = io.WriteString(w, `<div class="footer-note">Powered by Pinata • Reverse search via Tineye</div></body></html>`)
}

// Streaming search handler (no JS). Writes cards as results are decoded.
func searchHandler(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) < 1 || len(q) > 64 {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	bookmark := r.URL.Query().Get("bookmark")
	csrftoken := r.URL.Query().Get("csrftoken")

	dataObj := map[string]any{"options": map[string]any{"query": q}}
	if bookmark != "" {
		dataObj["options"].(map[string]any)["bookmarks"] = []string{bookmark}
	}
	jb, err := json.Marshal(dataObj)
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	dataParam := url.QueryEscape(string(jb))

	var req *http.Request
	if bookmark == "" {
		u := pinterestSearchURL + "?data=" + dataParam
		req, err = http.NewRequestWithContext(r.Context(), "GET", u, nil)
	} else {
		body := "data=" + dataParam
		req, err = http.NewRequestWithContext(r.Context(), "POST", pinterestSearchURL, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if err != nil {
		http.Error(w, "failed to build request", http.StatusInternalServerError)
		return
	}
	req.Header.Set("x-pinterest-pws-handler", "www/search/[scope].js")
	if csrftoken != "" {
		req.Header.Set("x-csrftoken", csrftoken)
		req.Header.Set("Cookie", "csrftoken="+csrftoken)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		http.Error(w, "failed to fetch", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	var newCsrf string
	for _, c := range resp.Cookies() {
		if strings.EqualFold(c.Name, "csrftoken") {
			newCsrf = c.Value
			break
		}
	}

	// Start streaming HTML
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>`+html.EscapeString(q)+` - Pinata</title><link rel="stylesheet" href="/static/style.css"></head><body>`)
	// header: inline search and Save-search form
	_, _ = io.WriteString(w, `<div class="header" style="margin-bottom:8px;"><a class="brand" href="/">Pinata</a><div class="search-box">`)
	_, _ = io.WriteString(w, `<form class="search-inline" method="get" action="/search"><input type="text" name="q" value="`+html.EscapeString(q)+`" maxlength="64"><button type="submit">Search</button></form>`)
	if bookmarkingEnabled {
		next := "/search?q=" + url.QueryEscape(q)
		_, _ = io.WriteString(w, `<form method="post" action="/bookmark" style="margin-left:8px;"><input type="hidden" name="q" value="`+html.EscapeString(q)+`"><input type="hidden" name="next" value="`+html.EscapeString(next)+`"><button class="btn-save" type="submit">Save</button></form>`)
	}
	_, _ = io.WriteString(w, `</div></div>`)
	_, _ = io.WriteString(w, `<h2 style="margin:4px 0 0 0;">Results for "`+html.EscapeString(q)+`"</h2>`)
	_, _ = io.WriteString(w, `<div class="img-container">`)

	dec := json.NewDecoder(resp.Body)
	var nextBookmark string

	for {
		tk, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				break
			}
			log.Printf("json token error: %v", err)
			break
		}
		key, ok := tk.(string)
		if !ok {
			continue
		}
		switch key {
		case "results":
			t, err := dec.Token()
			if err != nil {
				log.Printf("unexpected json after results: %v", err)
				continue
			}
			if delim, ok := t.(json.Delim); !ok || delim != '[' {
				continue
			}
			var rObj struct {
				Images struct {
					Orig struct {
						URL string `json:"url"`
					} `json:"orig"`
				} `json:"images"`
			}
			for dec.More() {
				if err := dec.Decode(&rObj); err != nil {
					log.Printf("error decoding result item: %v", err)
					break
				}
				u := strings.TrimSpace(rObj.Images.Orig.URL)
				if u == "" {
					continue
				}
				esc := url.QueryEscape(u)
				b64 := base64.StdEncoding.EncodeToString([]byte(u))

				// Write card (with magnifier optional and Save image form)
				_, _ = io.WriteString(w, `<div class="card">`)
				_, _ = io.WriteString(w, `<a href="/image_proxy?url=`+esc+`" style="display:block;"><img loading="lazy" src="/image_proxy?url=`+esc+`" alt="image"></a>`)
				_, _ = io.WriteString(w, `<div class="card-controls">`)
				if !disableReverse {
					_, _ = io.WriteString(w, `<a class="magnifier" href="/revsearch?b64=`+b64+`" title="Search Tineye" target="_blank">🔍</a>`)
				}
				if bookmarkingEnabled {
					next := "/search?q=" + url.QueryEscape(q)
					_, _ = io.WriteString(w, `<form method="post" action="/bookmark_image" style="display:inline;margin:0;"><input type="hidden" name="url" value="`+html.EscapeString(u)+`"><input type="hidden" name="next" value="`+html.EscapeString(next)+`"><button class="btn-save-mini" type="submit" title="Save image">❤</button></form>`)
				}
				_, _ = io.WriteString(w, `</div>`) // card-controls
				_, _ = io.WriteString(w, `</div>`) // card

				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
			}
			_, _ = dec.Token()
		case "bookmark":
			t, err := dec.Token()
			if err == nil {
				if s, ok := t.(string); ok {
					nextBookmark = s
				}
			}
		default:
			continue
		}
	}

	// finish results
	_, _ = io.WriteString(w, `</div>`)
	if nextBookmark != "" {
		qenc := url.QueryEscape(q)
		benc := url.QueryEscape(nextBookmark)
		cenc := ""
		if newCsrf != "" {
			cenc = "&csrftoken=" + url.QueryEscape(newCsrf)
		} else if csrftoken != "" {
			cenc = "&csrftoken=" + url.QueryEscape(csrftoken)
		}
		next := "/search?q=" + qenc + "&bookmark=" + benc + cenc
		_, _ = io.WriteString(w, `<div class="pagination"><a href="`+html.EscapeString(next)+`">Next page</a></div>`)
	}
	_, _ = io.WriteString(w, `<div class="footer-note">Powered by Pinata • Reverse search via Tineye</div></body></html>`)
}

// --------------------------------------------------------------------
// Bookmark endpoints (server-side, no JS)
// --------------------------------------------------------------------

func bookmarkPostHandler(w http.ResponseWriter, r *http.Request) {
	if !bookmarkingEnabled {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	q := strings.TrimSpace(r.FormValue("q"))
	if q == "" || len(q) > 64 {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	next := r.FormValue("next")
	if next == "" {
		next = "/"
	}
	entries := readBookmarksFromReq(r)
	new := []BookmarkEntry{{Type: "q", Value: q}}
	for _, e := range entries {
		if e.Type == "q" && e.Value == q {
			continue
		}
		new = append(new, e)
		if len(new) >= maxBookmarks {
			break
		}
	}
	setBookmarksCookie(w, new)
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func bookmarkImagePostHandler(w http.ResponseWriter, r *http.Request) {
	if !bookmarkingEnabled {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	u := strings.TrimSpace(r.FormValue("url"))
	if u == "" || !(strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	next := r.FormValue("next")
	if next == "" {
		next = "/"
	}
	entries := readBookmarksFromReq(r)
	new := []BookmarkEntry{{Type: "img", Value: u}}
	for _, e := range entries {
		if e.Type == "img" && e.Value == u {
			continue
		}
		new = append(new, e)
		if len(new) >= maxBookmarks {
			break
		}
	}
	setBookmarksCookie(w, new)
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func bookmarkRemoveHandler(w http.ResponseWriter, r *http.Request) {
	if !bookmarkingEnabled {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	typ := r.FormValue("type")
	val := r.FormValue("value")
	if typ == "" || val == "" {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	entries := readBookmarksFromReq(r)
	out := make([]BookmarkEntry, 0, len(entries))
	for _, e := range entries {
		if e.Type == typ && e.Value == val {
			continue
		}
		out = append(out, e)
	}
	if len(out) == 0 {
		clearBookmarksCookie(w)
	} else {
		setBookmarksCookie(w, out)
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func bookmarksExportHandler(w http.ResponseWriter, r *http.Request) {
	if !bookmarkingEnabled {
		http.Error(w, "bookmarks disabled", http.StatusNotFound)
		return
	}
	entries := readBookmarksFromReq(r)
	if entries == nil {
		entries = []BookmarkEntry{}
	}
	js, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		http.Error(w, "failed to export", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\"pinata_bookmarks.json\"")
	_, _ = w.Write(js)
}

func bookmarksImportHandler(w http.ResponseWriter, r *http.Request) {
	if !bookmarkingEnabled {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20) // 2MB
	if err := r.ParseMultipartForm(2 << 20); err != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	defer file.Close()
	dec := json.NewDecoder(file)
	var entries []BookmarkEntry
	if err := dec.Decode(&entries); err == nil {
		// ok
	} else {
		if _, err := file.Seek(0, io.SeekStart); err == nil {
			var arr []string
			dec2 := json.NewDecoder(file)
			if err2 := dec2.Decode(&arr); err2 == nil {
				entries = make([]BookmarkEntry, 0, len(arr))
				for _, s := range arr {
					entries = append(entries, BookmarkEntry{Type: "q", Value: s})
				}
			} else {
				http.Redirect(w, r, "/", http.StatusSeeOther)
				return
			}
		} else {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
	}
	existing := readBookmarksFromReq(r)
	merged := make([]BookmarkEntry, 0, maxBookmarks)
	seen := map[string]bool{}
	add := func(e BookmarkEntry) {
		key := e.Type + "|" + e.Value
		if seen[key] {
			return
		}
		seen[key] = true
		merged = append(merged, e)
	}
	for _, e := range entries {
		e.Value = strings.TrimSpace(e.Value)
		if e.Value == "" {
			continue
		}
		if len(e.Value) > maxItemLen {
			e.Value = e.Value[:maxItemLen]
		}
		if e.Type != "q" && e.Type != "img" {
			e.Type = "q"
		}
		add(e)
		if len(merged) >= maxBookmarks {
			break
		}
	}
	for _, e := range existing {
		add(e)
		if len(merged) >= maxBookmarks {
			break
		}
	}
	setBookmarksCookie(w, merged)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// --------------------------------------------------------------------
// Image proxy and reverse search
// --------------------------------------------------------------------
func imageProxyHandler(w http.ResponseWriter, r *http.Request) {
	uq := r.URL.Query().Get("url")
	if uq == "" {
		http.Error(w, "url required", http.StatusBadRequest)
		return
	}
	orig, err := url.QueryUnescape(uq)
	if err != nil || !(strings.HasPrefix(orig, "http://") || strings.HasPrefix(orig, "https://")) {
		http.Error(w, "invalid url", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", orig, nil)
	if err != nil {
		http.Error(w, "failed", http.StatusBadGateway)
		return
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64; rv:144.0) Gecko/20100101 Firefox/144.0")
	resp, err := httpClient.Do(req)
	if err != nil {
		http.Error(w, "failed to fetch", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "" {
		w.Header().Set("Cache-Control", cc)
	}
	w.WriteHeader(resp.StatusCode)
	bufPtr := copyBufPool.Get().(*[]byte)
	buf := *bufPtr
	_, _ = io.CopyBuffer(w, resp.Body, buf)
	copyBufPool.Put(bufPtr)
}

func revsearchHandler(w http.ResponseWriter, r *http.Request) {
	if disableReverse {
		http.Error(w, "reverse disabled", http.StatusNotFound)
		return
	}
	b64 := r.URL.Query().Get("b64")
	if b64 == "" {
		http.Error(w, "b64 required", http.StatusBadRequest)
		return
	}
	bs, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		http.Error(w, "invalid b64", http.StatusBadRequest)
		return
	}
	orig := string(bs)
	if !(strings.HasPrefix(orig, "http://") || strings.HasPrefix(orig, "https://")) {
		http.Error(w, "invalid url", http.StatusBadRequest)
		return
	}
	tineye := "https://tineye.com/search?url=" + url.QueryEscape(orig)
	http.Redirect(w, r, tineye, http.StatusSeeOther)
}

// --------------------------------------------------------------------
// main: route setup
// --------------------------------------------------------------------
func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/static/style.css", styleHandler)
	mux.HandleFunc("/", indexHandler)
	mux.HandleFunc("/search", searchHandler)
	mux.HandleFunc("/image_proxy", imageProxyHandler)
	mux.HandleFunc("/revsearch", revsearchHandler)

	// bookmark endpoints
	mux.HandleFunc("/bookmark", bookmarkPostHandler)
	mux.HandleFunc("/bookmark_image", bookmarkImagePostHandler)
	mux.HandleFunc("/bookmark_remove", bookmarkRemoveHandler)
	mux.HandleFunc("/bookmarks/export", bookmarksExportHandler)
	mux.HandleFunc("/bookmarks/import", bookmarksImportHandler)

	server := &http.Server{
		Addr:         ":8080",
		Handler:      mux,
		ReadTimeout:  12 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	log.Println("Pinata listening on :8080. Bookmarking enabled:", bookmarkingEnabled, " Reverse disabled:", disableReverse)
	log.Fatal(server.ListenAndServe())
}
