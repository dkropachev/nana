package gocli

import (
	"embed"
	"io/fs"
	"net/http"
	"net/url"
	"strings"
)

//go:embed start_ui_assets/* start_ui_assets/vendor/*
var startUIAssetsFS embed.FS

func startUIWebHandler(apiURL string) http.Handler {
	subtree, _ := fs.Sub(startUIAssetsFS, "start_ui_assets")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/" || r.URL.Path == "/index.html":
			content, err := fs.ReadFile(subtree, "index.html")
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			resolvedAPIURL := resolveStartUIAPIURLForRequest(apiURL, r.Host)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(strings.ReplaceAll(string(content), "__API_BASE__", resolvedAPIURL)))
		case r.URL.Path == "/app.css":
			http.ServeFileFS(w, r, subtree, "app.css")
		case r.URL.Path == "/app.js":
			w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
			http.ServeFileFS(w, r, subtree, "app.txt")
		case r.URL.Path == "/vendor/teryx.css":
			http.ServeFileFS(w, r, subtree, "vendor/teryx.css")
		case r.URL.Path == "/vendor/teryx.js":
			w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
			http.ServeFileFS(w, r, subtree, "vendor/teryx.txt")
		case r.URL.Path == "/vendor/xhtmlx.js":
			w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
			http.ServeFileFS(w, r, subtree, "vendor/xhtmlx.txt")
		default:
			http.NotFound(w, r)
		}
	})
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
