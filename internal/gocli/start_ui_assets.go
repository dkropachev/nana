package gocli

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"net/http"
	"net/url"
	"strings"
)

//go:embed start_ui_assets/* start_ui_assets/vendor/*
var startUIAssetsFS embed.FS

var startUIStaticAssetVersion = mustStartUIStaticAssetVersion()

func startUIWebHandler(apiURL string) http.Handler {
	subtree, _ := fs.Sub(startUIAssetsFS, "start_ui_assets")
	assetReplacer := strings.NewReplacer(
		`href="/vendor/teryx.css"`, `href="/vendor/teryx.css?v=`+startUIStaticAssetVersion+`"`,
		`href="/app.css"`, `href="/app.css?v=`+startUIStaticAssetVersion+`"`,
		`src="/vendor/xhtmlx.js"`, `src="/vendor/xhtmlx.js?v=`+startUIStaticAssetVersion+`"`,
		`src="/vendor/teryx.js"`, `src="/vendor/teryx.js?v=`+startUIStaticAssetVersion+`"`,
		`src="/app.js"`, `src="/app.js?v=`+startUIStaticAssetVersion+`"`,
	)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/" || r.URL.Path == "/index.html":
			content, err := fs.ReadFile(subtree, "index.html")
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			resolvedAPIURL := resolveStartUIAPIURLForRequest(apiURL, r.Host)
			setStartUIHTMLCacheHeaders(w)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			page := strings.ReplaceAll(string(content), "__API_BASE__", resolvedAPIURL)
			_, _ = w.Write([]byte(assetReplacer.Replace(page)))
		case r.URL.Path == "/app.css":
			setStartUIVersionedAssetCacheHeaders(w)
			http.ServeFileFS(w, r, subtree, "app.css")
		case r.URL.Path == "/app.js":
			setStartUIVersionedAssetCacheHeaders(w)
			w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
			http.ServeFileFS(w, r, subtree, "app.txt")
		case r.URL.Path == "/vendor/teryx.css":
			setStartUIVersionedAssetCacheHeaders(w)
			http.ServeFileFS(w, r, subtree, "vendor/teryx.css")
		case r.URL.Path == "/vendor/teryx.js":
			setStartUIVersionedAssetCacheHeaders(w)
			w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
			http.ServeFileFS(w, r, subtree, "vendor/teryx.txt")
		case r.URL.Path == "/vendor/xhtmlx.js":
			setStartUIVersionedAssetCacheHeaders(w)
			w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
			http.ServeFileFS(w, r, subtree, "vendor/xhtmlx.txt")
		default:
			http.NotFound(w, r)
		}
	})
}

func setStartUIHTMLCacheHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
}

func setStartUIVersionedAssetCacheHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
}

func mustStartUIStaticAssetVersion() string {
	paths := []string{
		"start_ui_assets/index.html",
		"start_ui_assets/app.css",
		"start_ui_assets/app.txt",
		"start_ui_assets/vendor/teryx.css",
		"start_ui_assets/vendor/teryx.txt",
		"start_ui_assets/vendor/xhtmlx.txt",
	}
	hash := sha256.New()
	for _, path := range paths {
		content, err := startUIAssetsFS.ReadFile(path)
		if err != nil {
			panic(err)
		}
		_, _ = hash.Write([]byte(path))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(content)
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))[:12]
}

func resolveStartUIAPIURLForRequest(apiURL string, requestHost string) string {
	parsed, err := url.Parse(strings.TrimSpace(apiURL))
	if err != nil {
		return apiURL
	}
	apiHost := parsed.Hostname()
	if apiHost != "" && apiHost != "0.0.0.0" && apiHost != "::" && apiHost != "[::]" {
		return apiURL
	}
	hostOnly := strings.TrimSpace(requestHost)
	if hostOnly == "" {
		return apiURL
	}
	if parsedPort := strings.TrimSpace(parsed.Port()); parsedPort != "" {
		if requestURL, requestErr := url.Parse("http://" + hostOnly); requestErr == nil {
			parsed.Host = requestURL.Hostname() + ":" + parsedPort
			return parsed.String()
		}
	}
	return apiURL
}
