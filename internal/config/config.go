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

type DatabaseConfig struct {
	User           string
	Password       string
	Host           string
	Port           string
	Name           string
	MaxConnections int32
}

type MessagingConfig struct {
	Endpoint       string
	Region         string
	InboxQueueURL  string
	EventsTopicARN string
	PollWait       time.Duration
}

type MercadoPagoConfig struct {
	BaseURL       string
	AccessToken   string
	WebhookSecret string
}

type Config struct {
	Server      *ServerConfig
	Database    *DatabaseConfig
	Messaging   *MessagingConfig
	MercadoPago *MercadoPagoConfig
}

func Load() (*Config, error) {
	v := viper.New()
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()
	v.SetDefault("SERVER_PORT", "8080")
	v.SetDefault("DATABASE_MAX_CONNECTIONS", 5)
	v.SetDefault("AWS_DEFAULT_REGION", "sa-east-1")
	v.SetDefault("MESSAGING_POLL_WAIT", "20s")
	v.SetDefault("MERCADOPAGO_BASE_URL", "https://api.mercadopago.com")

	return &Config{
		Server: &ServerConfig{
			Environment: v.GetString("SERVER_ENVIRONMENT"),
			Port:        v.GetString("SERVER_PORT"),
			BaseURL:     v.GetString("APP_BASE_URL"),
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
			Endpoint:       v.GetString("AWS_ENDPOINT_URL"),
			Region:         v.GetString("AWS_DEFAULT_REGION"),
			InboxQueueURL:  v.GetString("MESSAGING_INBOX_QUEUE_URL"),
			EventsTopicARN: v.GetString("MESSAGING_EVENTS_TOPIC_ARN"),
			PollWait:       v.GetDuration("MESSAGING_POLL_WAIT"),
		},
		MercadoPago: &MercadoPagoConfig{
			BaseURL:       v.GetString("MERCADOPAGO_BASE_URL"),
			AccessToken:   v.GetString("MERCADOPAGO_ACCESS_TOKEN"),
			WebhookSecret: v.GetString("MERCADOPAGO_WEBHOOK_SECRET"),
		},
	}, nil
}
