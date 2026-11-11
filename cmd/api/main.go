package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/billing"
	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/config"
	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/database"
	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/mercadopago"
	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/messaging"
	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/messaging/pgoutbox"
	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/observability"
	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/routes"
	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/packages/email"
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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	stopTracer := observability.StartTracer(slog.Default())
	defer stopTracer()

	db, err := database.NewConnection(cfg.Database)
	if err != nil {
		exit(err, "failed to connect to database", observability.Integration(observability.IntegrationRDS))
	}
	defer db.Close()

	sqsClient, snsClient, err := messaging.NewClients(ctx, cfg.Messaging.Region, cfg.Messaging.Endpoint)
	if err != nil {
		exit(err, "failed to create messaging clients", observability.Integration(observability.IntegrationSQS))
	}

	mailer, err := email.New(cfg.Email.Provider, email.Config{
		Host:           cfg.Email.Host,
		Port:           cfg.Email.Port,
		From:           cfg.Email.From,
		Region:         cfg.Email.Region,
		SESSenderEmail: cfg.Email.SESSenderEmail,
		SESReplyTo:     cfg.Email.SESReplyTo,
		SESConfigSet:   cfg.Email.SESConfigSet,
	})
	if err != nil {
		slog.LogAttrs(ctx, slog.LevelError, "e-mail provider unavailable, customer e-mails disabled", observability.Err(err))
		mailer = nil
	}

	svc := billing.NewService(
		db,
		mercadopago.NewClient(cfg.MercadoPago.BaseURL, cfg.MercadoPago.AccessToken, nil),
		billing.NewEmailNotifier(mailer, cfg.Server.BaseURL),
		billing.Config{
			EventsTopic:       cfg.Messaging.EventsTopicARN,
			NotificationURL:   cfg.MercadoPago.NotificationURL,
			PaymentExpiration: cfg.MercadoPago.PaymentExpiration,
			WebhookSecret:     cfg.MercadoPago.WebhookSecret,
		},
		slog.Default(),
	)

	var workers sync.WaitGroup
	relay := messaging.NewRelay(pgoutbox.NewStore(db), messaging.NewAWSPublisher(sqsClient, snsClient),
		cfg.Messaging.OutboxPollInterval, slog.Default())
	workers.Go(func() { relay.Run(ctx) })

	if cfg.Messaging.InboxQueueURL != "" {
		consumer := messaging.NewConsumer(sqsClient, cfg.Messaging.InboxQueueURL, cfg.Messaging.PollWait, svc.Handle, slog.Default())
		workers.Go(func() { consumer.Run(ctx) })
	} else {
		slog.Warn("MESSAGING_INBOX_QUEUE_URL not set, consumer disabled")
	}

	app := fiber.New()
	routes.Register(app, routes.Deps{DB: db, Billing: svc, JWTSecretKey: cfg.Auth.JWTSecretKey})

	listenErr := make(chan error, 1)
	go func() { listenErr <- app.Listen(":"+cfg.Server.Port, fiber.ListenConfig{DisableStartupMessage: true}) }()
	slog.Info("billing-service started", slog.String("port", cfg.Server.Port))

	select {
	case <-ctx.Done():
		slog.Info("shutdown requested")
	case err := <-listenErr:
		if err != nil {
			slog.LogAttrs(context.Background(), slog.LevelError, "http server stopped", observability.Err(err))
		}
		stop()
	}

	if err := app.ShutdownWithTimeout(10 * time.Second); err != nil {
		slog.LogAttrs(context.Background(), slog.LevelError, "http shutdown failed", observability.Err(err))
	}
	workers.Wait()
	slog.Info("billing-service stopped")
}

func exit(err error, message string, attrs ...slog.Attr) {
	slog.LogAttrs(context.Background(), slog.LevelError, message, append(attrs, observability.Err(err))...)
	os.Exit(1)
}
