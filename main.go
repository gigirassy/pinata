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
		b := make([]byte, 32*1024)
		return &b
	},
}

const pinterestSearchURL = "https://www.pinterest.com/resource/BaseSearchResource/get/"
const cookieName = "pinata_bm"

// -- bookmark encryption key (32 bytes) --
var bookmarkKey []byte
var bookmarkingEnabled bool
var disableReverse bool

func init() {
    if kb := os.Getenv("PINATA_BOOKMARK_KEY"); kb != "" {
        if decoded, err := base64.StdEncoding.DecodeString(kb); err == nil && len(decoded) == 32 {
            bookmarkKey = decoded
            bookmarkingEnabled = true
            log.Println("Bookmarking enabled")
        } else {
            log.Println("PINATA_BOOKMARK_KEY provided but invalid; bookmarking disabled")
            bookmarkingEnabled = false
        }
    } else {
        log.Println("PINATA_BOOKMARK_KEY not set; bookmarking disabled")
        bookmarkingEnabled = false
    }

    // New: whether to disable reverse image search (Tineye)
    disableEnv := strings.ToLower(strings.TrimSpace(os.Getenv("PINATA_DISABLE_REVERSE")))
    if disableEnv == "1" || disableEnv == "true" || disableEnv == "yes" {
        disableReverse = true
        log.Println("Reverse image search disabled via PINATA_DISABLE_REVERSE")
    } else {
        disableReverse = false
    }
}

// encrypt a JSON list of strings -> base64 url safe
func encryptBookmarks(items []string) (string, error) {
	plain, _ := json.Marshal(items)
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
	ciphertext := gcm.Seal(nonce, nonce, plain, nil)
	return base64.RawURLEncoding.EncodeToString(ciphertext), nil
}

// decrypt base64 cookie -> list of strings
func decryptBookmarks(encoded string) ([]string, error) {
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
		return nil, err
	}
	nonce := data[:ns]
	ct := data[ns:]
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, err
	}
	var items []string
	if err := json.Unmarshal(plain, &items); err != nil {
		return nil, err
	}
	return items, nil
}

// helper to read bookmarks from request cookie (returns empty slice if none or invalid)
func readBookmarksFromReq(r *http.Request) []string {
	if !bookmarkingEnabled {
		return nil
	}
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" {
		return nil
	}
	items, err := decryptBookmarks(c.Value)
	if err != nil {
		return nil
	}
	return items
}

// helper to set bookmarks cookie
func setBookmarksCookie(w http.ResponseWriter, items []string) {
	if !bookmarkingEnabled {
		return
	}
	// sanitize and truncate each item
	trunc := make([]string, 0, len(items))
	for _, s := range items {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if len(s) > 64 {
			s = s[:64]
		}
		trunc = append(trunc, s)
		if len(trunc) >= 30 {
			break
		}
	}
	enc, err := encryptBookmarks(trunc)
	if err != nil {
		// fail silently (do not set)
		return
	}
	c := &http.Cookie{
		Name:     cookieName,
		Value:    enc,
		Path:     "/",
		HttpOnly: true,              // not accessible to JS (we have no JS but keeps it private)
		SameSite: http.SameSiteLaxMode,
		// Secure:   true, // keep commented for local HTTP dev - set true in production behind TLS
		MaxAge: 60 * 60 * 24 * 365 * 10, // ~10 years
	}
	http.SetCookie(w, c)
}

// remove bookmark and reset cookie
func removeBookmarkCookie(w http.ResponseWriter, items []string) {
	// if empty, clear cookie
	if len(items) == 0 {
		c := &http.Cookie{
			Name:     cookieName,
			Value:    "",
			Path:     "/",
			HttpOnly: true,
			MaxAge:   -1,
		}
		http.SetCookie(w, c)
		return
	}
	setBookmarksCookie(w, items)
}

// ----- CSS and HTML (no JS) -----
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
.magnifier{position:absolute;top:8px;right:8px;background:rgba(0,0,0,0.45);border:1px solid rgba(255,255,255,0.06);color:var(--text);padding:6px;border-radius:999px;font-size:14px;width:34px;height:34px;display:inline-flex;align-items:center;justify-content:center;text-decoration:none;transition:transform .12s ease,background .12s ease}
.magnifier:hover{transform:translateY(-2px);background:linear-gradient(180deg,rgba(124,58,237,0.14),rgba(124,58,237,0.08));color:white}
.bookmarks{margin-left:12px;color:var(--muted);font-size:14px}
.bookmark-list{margin-top:10px;display:flex;gap:8px;flex-wrap:wrap}
.bookmark-pill{background:rgba(255,255,255,0.03);padding:6px 8px;border-radius:999px;border:1px solid rgba(255,255,255,0.04);font-size:13px;display:flex;gap:6px;align-items:center}
.bookmark-pill form{display:inline}
.bookmark-remove-btn{background:transparent;border:none;color:#ff7b7b;font-weight:700;cursor:pointer;padding:0 6px}
.pagination{text-align:center;margin:26px 0}
.pagination a{color:var(--accent);text-decoration:none;padding:8px 12px;border-radius:8px;border:1px solid rgba(124,58,237,0.12);background:rgba(124,58,237,0.02)}
.footer-note{color:var(--muted);font-size:12px;margin-top:22px}
@media (max-width:640px){
  body{padding:12px;font-size:18px}
  .brand{font-size:22px}
  input[type="text"]{min-width:120px;padding:12px 14px;font-size:16px}
  button[type="submit"],.btn-save{padding:10px 14px;font-size:16px;border-radius:10px}
  .img-container{column-width:180px;column-gap:12px}
  .search-block{gap:10px;flex-direction:column}
  .search-inline{width:100%}
  .search-box{margin-left:0;width:100%}
  .bookmarks{order:3;width:100%;margin-top:8px}
}
`

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
}

// Search streaming handler (same streaming approach as before), includes server-side Save form when bookmarkingEnabled
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

	// start streaming HTML
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	io.WriteString(w, `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>`+html.EscapeString(q)+` - Pinata</title><link rel="stylesheet" href="/static/style.css"></head><body>`)
	// header with inline search and server-side Save form (if enabled)
	io.WriteString(w, `<div class="header" style="margin-bottom:8px;"><a class="brand" href="/">Pinata</a><div class="search-box">`)
	// inline search form
	io.WriteString(w, `<form class="search-inline" method="get" action="/search"><input type="text" name="q" value="`+html.EscapeString(q)+`" maxlength="64"><button type="submit">Search</button></form>`)
	// Save form: POSTs to /bookmark with q and next back to the current results page
	if bookmarkingEnabled {
		// next param to return to this search page (use URL-encoded /search?q=...)
		next := "/search?q=" + url.QueryEscape(q)
		io.WriteString(w, `<form method="post" action="/bookmark" style="margin-left:8px;"><input type="hidden" name="q" value="`+html.EscapeString(q)+`"><input type="hidden" name="next" value="`+html.EscapeString(next)+`"><button class="btn-save" type="submit">Save</button></form>`)
	}
	io.WriteString(w, `</div></div>`)
	io.WriteString(w, `<h2 style="margin:4px 0 0 0;">Results for "`+html.EscapeString(q)+`"</h2>`)
	io.WriteString(w, `<div class="img-container">`)

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
				var cardBuilder strings.Builder
				cardBuilder.WriteString(`<div class="card"><a href="/image_proxy?url=` + esc + `" style="display:block;"><img loading="lazy" src="/image_proxy?url=` + esc + `" alt="image"></a>`)
				if !disableReverse {
 					cardBuilder.WriteString(`<a class="magnifier" href="/revsearch?b64=` + b64 + `" title="Search Tineye" target="_blank">🔍</a>`)
				}
				cardBuilder.WriteString(`</div>`)
				io.WriteString(w, cardBuilder.String())
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
			}
			_, _ = dec.Token() // consume closing
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

	// close container
	io.WriteString(w, `</div>`)

	// show pagination link if present
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
		io.WriteString(w, `<div class="pagination"><a href="`+html.EscapeString(next)+`">Next page</a></div>`)
	}

	io.WriteString(w, `<div class="footer-note">Powered by Pinata • Reverse search via Tineye</div></body></html>`)
}

// POST /bookmark : server-side saving of a bookmark (no JS). expects form q and optional next.
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
	// read existing
	items := readBookmarksFromReq(r)
	// remove if exists then prepend
	newItems := make([]string, 0, 32)
	newItems = append(newItems, q)
	for _, v := range items {
		if v == q {
			continue
		}
		newItems = append(newItems, v)
		if len(newItems) >= 30 {
			break
		}
	}
	setBookmarksCookie(w, newItems)
	http.Redirect(w, r, next, http.StatusSeeOther)
}

// POST /bookmark_remove : remove a bookmark (form q)
func bookmarkRemoveHandler(w http.ResponseWriter, r *http.Request) {
	if !bookmarkingEnabled {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	q := strings.TrimSpace(r.FormValue("q"))
	items := readBookmarksFromReq(r)
	if len(items) == 0 {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	out := make([]string, 0, len(items))
	for _, v := range items {
		if v == q {
			continue
		}
		out = append(out, v)
	}
	// set or clear cookie
	removeBookmarkCookie(w, out)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// imageProxy uses pooled buffer for io.CopyBuffer
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

// revsearch redirect to tineye
func revsearchHandler(w http.ResponseWriter, r *http.Request) {
	if disableReverse {
        	http.Error(w, "Reverse image search disabled", http.StatusNotFound)
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

// static CSS
func styleHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	io.WriteString(w, cssContent)
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/static/style.css", styleHandler)
	mux.HandleFunc("/", indexHandler)
	mux.HandleFunc("/search", searchHandler)
	mux.HandleFunc("/image_proxy", imageProxyHandler)
	mux.HandleFunc("/revsearch", revsearchHandler)
	// bookmark endpoints (POST)
	mux.HandleFunc("/bookmark", bookmarkPostHandler)
	mux.HandleFunc("/bookmark_remove", bookmarkRemoveHandler)

	srv := &http.Server{
		Addr:         ":8080",
		Handler:      mux,
		ReadTimeout:  12 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	log.Println("Pinata listening on :8080")
	log.Fatal(srv.ListenAndServe())
}
