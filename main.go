// main.go
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"html"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ------------------- Config & globals -------------------
var httpClient = &http.Client{
	Timeout: 15 * time.Second,
	Transport: &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   8 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:        10, // small pool to reduce memory
		MaxIdleConnsPerHost: 6,
		IdleConnTimeout:     60 * time.Second,
		TLSHandshakeTimeout: 8 * time.Second,
	},
}

const pinterestSearchURL = "https://www.pinterest.com/resource/BaseSearchResource/get/"

// ------------------- Static CSS & HTML header/footer -------------------
const cssContent = `
:root{--bg:#0b0f17;--muted:#94a3b8;--text:#e6e6ff;--accent:#7c3aed;--card-shadow:rgba(0,0,0,0.6)}
*{box-sizing:border-box}body{margin:0;padding:20px;background:linear-gradient(180deg,#071020 0%,#0b0f17 100%);color:var(--text);font-family:ui-monospace,Menlo,Monaco,monospace}
.header{display:flex;gap:12px;align-items:center;margin-bottom:18px}.brand{font-size:20px;font-weight:700;color:var(--accent);text-decoration:none}.search-box{margin-left:auto;display:flex;gap:8px;align-items:center}input[type="text"]{background:transparent;border:1px solid rgba(255,255,255,0.06);padding:8px 12px;color:var(--text);min-width:240px;border-radius:8px;outline:none}button[type="submit"]{background:linear-gradient(90deg,var(--accent),#5b21b6);color:white;border:none;padding:8px 12px;border-radius:8px;cursor:pointer}
.img-container{display:grid;grid-template-columns:repeat(auto-fill,minmax(220px,1fr));gap:16px;align-items:start;margin-top:18px}
.card{position:relative;border-radius:10px;overflow:hidden;background:linear-gradient(180deg,rgba(255,255,255,0.01),rgba(255,255,255,0.02));box-shadow:0 6px 18px rgba(3,7,18,0.6);border:1px solid rgba(124,58,237,0.06)}
.card img{display:block;width:100%;height:auto;object-fit:cover;background:#08101a}
.magnifier{position:absolute;top:8px;right:8px;background:rgba(0,0,0,0.45);border:1px solid rgba(255,255,255,0.06);color:var(--text);padding:6px;border-radius:999px;font-size:14px;width:34px;height:34px;display:inline-flex;align-items:center;justify-content:center;text-decoration:none;transition:transform .12s ease,background .12s ease}
.magnifier:hover{transform:translateY(-2px);background:linear-gradient(180deg,rgba(124,58,237,0.14),rgba(124,58,237,0.08));color:white;cursor:pointer}
.pagination{text-align:center;margin:26px 0}.pagination a{color:var(--accent);text-decoration:none;padding:8px 12px;border-radius:8px;border:1px solid rgba(124,58,237,0.12);background:rgba(124,58,237,0.02)}
.footer-note{color:var(--muted);font-size:12px;margin-top:22px}
`

const indexHTML = `<!doctype html>
<html>
<head>
<meta charset="utf-8">
<title>Pinata - Search</title>
<link rel="stylesheet" href="/static/style.css">
</head>
<body>
<div class="header">
  <a class="brand" href="/">Pinata</a>
  <div class="search-box">
    <form method="get" action="/search" style="display:flex;gap:8px;align-items:center;">
      <input type="text" name="q" placeholder="Search Image" required maxlength="64">
      <button type="submit">Search</button>
    </form>
  </div>
</div>
<div style="color:#94a3b8">Search images from Pinterest — click an image to open, or use the magnifier to search Tineye.</div>
</body>
</html>
`

// header for streamed results; we write query directly escaped
func resultsHeader(query string) string {
	return `<!doctype html>
<html>
<head>
<meta charset="utf-8">
<title>` + html.EscapeString(query) + ` - Pinata</title>
<link rel="stylesheet" href="/static/style.css">
</head>
<body>
<div class="header" style="margin-bottom:8px;">
  <a class="brand" href="/">Pinata</a>
  <div style="margin-left:auto;">
    <form method="get" action="/search" style="display:flex;gap:8px;align-items:center;">
      <input type="text" name="q" value="` + html.EscapeString(query) + `" maxlength="64">
      <button type="submit">Search</button>
    </form>
  </div>
</div>
<h2 style="margin:4px 0 0 0;">Results for "` + html.EscapeString(query) + `"</h2>
<div class="img-container">`
}

const resultsFooterPrefix = `</div>` // close img-container; will append pagination and footer after streaming

const footerHTML = `
{{PAGINATION}}
<div class="footer-note">Powered by Pinata • Reverse search via Tineye</div>
</body>
</html>
`

// ------------------- Handlers -------------------
func styleHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	_, _ = w.Write([]byte(cssContent))
}

func indexHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(indexHTML))
}

// searchHandler streams Pinterest response and writes each card as it's decoded.
// It captures csrftoken cookie and bookmark; bookmark is used to render the "Next" link at the end.
func searchHandler(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) < 1 || len(q) > 64 {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	bookmark := r.URL.Query().Get("bookmark")
	csrftoken := r.URL.Query().Get("csrftoken")

	// Build data param JSON (same structure as PHP)
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

	// capture csrftoken if set by server (we return it via pagination link)
	var newCsrf string
	for _, c := range resp.Cookies() {
		if strings.EqualFold(c.Name, "csrftoken") {
			newCsrf = c.Value
			break
		}
	}

	// Start streaming HTML
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(resultsHeader(q)))

	dec := json.NewDecoder(resp.Body)

	// We'll find "results" array and iterate it; also capture "bookmark" value
	var nextBookmark string

	// Use token streaming to find the "results" key and the "bookmark" key.
	for {
		tk, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				break
			}
			// parsing error: finish gracefully
			log.Printf("json token error: %v", err)
			break
		}
		// We're looking for string keys
		key, ok := tk.(string)
		if !ok {
			continue
		}
		switch key {
		case "results":
			// next token should be '['
			t, err := dec.Token()
			if err != nil {
				log.Printf("unexpected json after results: %v", err)
				continue
			}
			if delim, ok := t.(json.Delim); !ok || delim != '[' {
				// not an array, continue
				continue
			}
			// iterate results array: decode each result object into a small struct
			var rObj struct {
				Images struct {
					Orig struct {
						URL string `json:"url"`
					} `json:"orig"`
				} `json:"images"`
			}
			for dec.More() {
				// decode one result; this allocates a very small struct each time
				if err := dec.Decode(&rObj); err != nil {
					log.Printf("error decoding result item: %v", err)
					break
				}
				u := strings.TrimSpace(rObj.Images.Orig.URL)
				if u == "" {
					continue
				}
				// write card HTML for this image (escape values)
				esc := url.QueryEscape(u)
				b64 := base64.StdEncoding.EncodeToString([]byte(u))
				// card html
				_, _ = io.WriteString(w, `<div class="card">`)
				_, _ = io.WriteString(w, `<a href="/image_proxy?url=`+esc+`" style="display:block;">`)
				_, _ = io.WriteString(w, `<img loading="lazy" src="/image_proxy?url=`+esc+`" alt="image">`)
				_, _ = io.WriteString(w, `</a>`)
				_, _ = io.WriteString(w, `<a class="magnifier" href="/revsearch?b64=`+b64+`" title="Search Tineye" target="_blank">🔍</a>`)
				_, _ = io.WriteString(w, `</div>`)
				// flush to client if possible (net/http does buffering internally)
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
			}
			// consume closing ']' token
			_, _ = dec.Token()
		case "bookmark":
			// next token should be the bookmark string (or null)
			t, err := dec.Token()
			if err == nil {
				if s, ok := t.(string); ok {
					nextBookmark = s
				}
			}
		default:
			// ignore other keys
			continue
		}
	}

	// Close the img-container
	_, _ = io.WriteString(w, resultsFooterPrefix)

	// Render pagination if we have a bookmark
	if nextBookmark != "" {
		// Build next link with csrftoken if present
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

	// Footer
	_, _ = io.WriteString(w, `<div class="footer-note">Powered by Pinata • Reverse search via Tineye</div>`)
	_, _ = io.WriteString(w, `</body></html>`)

	// done
}

// imageProxyHandler streams remote image bodies directly to the client (no buffering)
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

	// copy only needed headers
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "" {
		w.Header().Set("Cache-Control", cc)
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// revsearchHandler decodes base64 and redirects to Tineye
func revsearchHandler(w http.ResponseWriter, r *http.Request) {
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

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/static/style.css", styleHandler)
	mux.HandleFunc("/", indexHandler)
	mux.HandleFunc("/search", searchHandler)
	mux.HandleFunc("/image_proxy", imageProxyHandler)
	mux.HandleFunc("/revsearch", revsearchHandler)

	srv := &http.Server{
		Addr:         ":8080",
		Handler:      mux,
		ReadTimeout:  12 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	log.Println("Pinata listening on :8080 (streaming mode, low allocations)")
	log.Fatal(srv.ListenAndServe())
}
