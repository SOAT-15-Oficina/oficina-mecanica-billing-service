package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/config"
	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/database"
	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/observability"
	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/routes"
	"github.com/gofiber/fiber/v3"
)

func main() {
	observability.Setup()

	cfg, err := config.Load()
	if err != nil {
		exit(err, "failed to load configuration")
	}

	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		migrate(cfg)
		return
	}
	serve(cfg)
}

func migrate(cfg *config.Config) {
	db, err := database.NewConnection(cfg.Database)
	if err != nil {
		exit(err, "failed to connect to database", observability.Integration(observability.IntegrationRDS))
	}
	defer db.Close()

	if err := database.RunMigrations(db); err != nil {
		exit(err, "failed to run migrations", observability.Integration(observability.IntegrationRDS))
	}
	slog.Info("migrations applied successfully")
}

func serve(cfg *config.Config) {
	stopTracer := observability.StartTracer(slog.Default())
	defer stopTracer()

	db, err := database.NewConnection(cfg.Database)
	if err != nil {
		exit(err, "failed to connect to database", observability.Integration(observability.IntegrationRDS))
	}
	defer db.Close()

	app := fiber.New()
	routes.Register(app, db)

	if err := app.Listen(":" + cfg.Server.Port); err != nil {
		exit(err, "failed to start server")
	}
}

func exit(err error, message string, attrs ...slog.Attr) {
	slog.LogAttrs(context.Background(), slog.LevelError, message, append(attrs, observability.Err(err))...)
	os.Exit(1)
}
