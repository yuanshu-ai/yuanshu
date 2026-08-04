package server

import (
	"embed"
	"net/http"
)

//go:embed pairing-web/*
var pairingWeb embed.FS

func PairingPageHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /pair", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		raw, _ := pairingWeb.ReadFile("pairing-web/index.html")
		_, _ = w.Write(raw)
	})
	mux.HandleFunc("GET /pair/app.js", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		raw, _ := pairingWeb.ReadFile("pairing-web/app.js")
		_, _ = w.Write(raw)
	})
	mux.HandleFunc("GET /pair/session.js", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		raw, _ := pairingWeb.ReadFile("pairing-web/session.js")
		_, _ = w.Write(raw)
	})
	mux.HandleFunc("GET /pair/storage.js", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		raw, _ := pairingWeb.ReadFile("pairing-web/storage.js")
		_, _ = w.Write(raw)
	})
	mux.HandleFunc("GET /pair/catalog.generated.js", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		raw, _ := pairingWeb.ReadFile("pairing-web/catalog.generated.js")
		_, _ = w.Write(raw)
	})
	mux.HandleFunc("GET /pair/style.css", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		raw, _ := pairingWeb.ReadFile("pairing-web/style.css")
		_, _ = w.Write(raw)
	})
	mux.HandleFunc("GET /pair/logo.svg", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		raw, _ := pairingWeb.ReadFile("pairing-web/logo.svg")
		_, _ = w.Write(raw)
	})
	return mux
}
