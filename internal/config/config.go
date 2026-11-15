package config

import (
	"strings"
	"time"

	"github.com/spf13/viper"
)

type ServerConfig struct {
	Environment string
	Port        string
	BaseURL     string
}

type AuthConfig struct {
	JWTSecretKey string
}

type DatabaseConfig struct {
	User           string
	Password       string
	Host           string
	Port           string
	Name           string
	MaxConnections int32
}

type MessagingConfig struct {
	Endpoint           string
	Region             string
	InboxQueueURL      string
	EventsTopicARN     string
	PollWait           time.Duration
	OutboxPollInterval time.Duration
}

type MercadoPagoConfig struct {
	BaseURL           string
	AccessToken       string
	WebhookSecret     string
	POSExternalID     string
	PaymentExpiration time.Duration
}

type EmailConfig struct {
	Provider       string
	Host           string
	Port           int
	From           string
	Region         string
	SESSenderEmail string
	SESReplyTo     string
	SESConfigSet   string
}

type Config struct {
	Server      *ServerConfig
	Auth        *AuthConfig
	Database    *DatabaseConfig
	Messaging   *MessagingConfig
	MercadoPago *MercadoPagoConfig
	Email       *EmailConfig
}

func Load() (*Config, error) {
	v := viper.New()
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()
	v.SetDefault("SERVER_PORT", "8080")
	v.SetDefault("DATABASE_MAX_CONNECTIONS", 5)
	v.SetDefault("AWS_DEFAULT_REGION", "sa-east-1")
	v.SetDefault("MESSAGING_POLL_WAIT", "20s")
	v.SetDefault("OUTBOX_POLL_INTERVAL", "500ms")
	v.SetDefault("MERCADOPAGO_BASE_URL", "https://api.mercadopago.com")
	v.SetDefault("MERCADOPAGO_POS_EXTERNAL_ID", "OFICINAMATRIZPOS01")
	v.SetDefault("PAYMENT_EXPIRATION", "48h")
	v.SetDefault("EMAIL_PROVIDER", "mailhog")
	v.SetDefault("EMAIL_PORT", 1025)

	baseURL := strings.TrimRight(v.GetString("APP_BASE_URL"), "/")

	return &Config{
		Server: &ServerConfig{
			Environment: v.GetString("SERVER_ENVIRONMENT"),
			Port:        v.GetString("SERVER_PORT"),
			BaseURL:     baseURL,
		},
		Auth: &AuthConfig{
			JWTSecretKey: v.GetString("JWT_SECRET_KEY"),
		},
		Database: &DatabaseConfig{
			User:           v.GetString("DATABASE_USER"),
			Password:       v.GetString("DATABASE_PASSWORD"),
			Host:           v.GetString("DATABASE_HOST"),
			Port:           v.GetString("DATABASE_PORT"),
			Name:           v.GetString("DATABASE_NAME"),
			MaxConnections: int32(v.GetInt("DATABASE_MAX_CONNECTIONS")),
		},
		Messaging: &MessagingConfig{
			Endpoint:           v.GetString("AWS_ENDPOINT_URL"),
			Region:             v.GetString("AWS_DEFAULT_REGION"),
			InboxQueueURL:      v.GetString("MESSAGING_INBOX_QUEUE_URL"),
			EventsTopicARN:     v.GetString("MESSAGING_EVENTS_TOPIC_ARN"),
			PollWait:           v.GetDuration("MESSAGING_POLL_WAIT"),
			OutboxPollInterval: v.GetDuration("OUTBOX_POLL_INTERVAL"),
		},
		MercadoPago: &MercadoPagoConfig{
			BaseURL:           strings.TrimRight(v.GetString("MERCADOPAGO_BASE_URL"), "/"),
			AccessToken:       v.GetString("MERCADOPAGO_ACCESS_TOKEN"),
			WebhookSecret:     v.GetString("MERCADOPAGO_WEBHOOK_SECRET"),
			POSExternalID:     v.GetString("MERCADOPAGO_POS_EXTERNAL_ID"),
			PaymentExpiration: v.GetDuration("PAYMENT_EXPIRATION"),
		},
		Email: &EmailConfig{
			Provider:       v.GetString("EMAIL_PROVIDER"),
			Host:           v.GetString("EMAIL_HOST"),
			Port:           v.GetInt("EMAIL_PORT"),
			From:           v.GetString("EMAIL_FROM"),
			Region:         v.GetString("AWS_DEFAULT_REGION"),
			SESSenderEmail: v.GetString("SES_SENDER_EMAIL"),
			SESReplyTo:     v.GetString("SES_REPLY_TO"),
			SESConfigSet:   v.GetString("SES_CONFIG_SET"),
		},
	}, nil
}
