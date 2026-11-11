package main

import (
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/mpfake"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	port := env("PORT", "8095")
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		healthcheck(port)
		return
	}
	publicURL := env("MPFAKE_PUBLIC_URL", "http://localhost:"+port)
	server := mpfake.New(publicURL, os.Getenv("MERCADOPAGO_WEBHOOK_SECRET"), nil, logger)

	srv := &http.Server{Addr: ":" + port, Handler: server.Handler(), ReadHeaderTimeout: 5 * time.Second}
	logger.Info("mpfake: listening", slog.String("addr", srv.Addr), slog.String("public_url", publicURL))
	if err := srv.ListenAndServe(); err != nil {
		logger.Error("mpfake: stopped", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

func healthcheck(port string) {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/__health")
	if err != nil || resp.StatusCode != http.StatusOK {
		os.Exit(1)
	}
	_ = resp.Body.Close()
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
