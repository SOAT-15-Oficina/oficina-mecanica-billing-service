package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadAppliesDefaults(t *testing.T) {
	t.Setenv("APP_BASE_URL", "http://localhost:8080/api/")

	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, "8080", cfg.Server.Port)
	assert.Equal(t, "http://localhost:8080/api", cfg.Server.BaseURL)
	assert.Equal(t, int32(5), cfg.Database.MaxConnections)
	assert.Equal(t, "sa-east-1", cfg.Messaging.Region)
	assert.Equal(t, 20*time.Second, cfg.Messaging.PollWait)
	assert.Equal(t, 500*time.Millisecond, cfg.Messaging.OutboxPollInterval)
	assert.Equal(t, "https://api.mercadopago.com", cfg.MercadoPago.BaseURL)
	assert.Equal(t, "OFICINAMATRIZPOS01", cfg.MercadoPago.POSExternalID)
	assert.Equal(t, 48*time.Hour, cfg.MercadoPago.PaymentExpiration)
	assert.Equal(t, "mailhog", cfg.Email.Provider)
	assert.Equal(t, 1025, cfg.Email.Port)
	assert.Equal(t, "sa-east-1", cfg.Email.Region)
}

func TestLoadReadsEnvironment(t *testing.T) {
	t.Setenv("SERVER_PORT", "9090")
	t.Setenv("JWT_SECRET_KEY", "secret")
	t.Setenv("DATABASE_HOST", "billing-db")
	t.Setenv("MESSAGING_INBOX_QUEUE_URL", "http://localstack:4566/000000000000/billing-inbox.fifo")
	t.Setenv("MESSAGING_POLL_WAIT", "1s")
	t.Setenv("OUTBOX_POLL_INTERVAL", "2s")
	t.Setenv("MERCADOPAGO_BASE_URL", "http://mpfake:8095/")
	t.Setenv("MERCADOPAGO_ACCESS_TOKEN", "TEST-token")
	t.Setenv("MERCADOPAGO_POS_EXTERNAL_ID", "LOJA2POS01")
	t.Setenv("PAYMENT_EXPIRATION", "30s")
	t.Setenv("EMAIL_PROVIDER", "ses")
	t.Setenv("SES_SENDER_EMAIL", "oficina@example.com")
	t.Setenv("SES_REPLY_TO", "contato@example.com")
	t.Setenv("SES_CONFIG_SET", "oficina")

	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, "9090", cfg.Server.Port)
	assert.Equal(t, "secret", cfg.Auth.JWTSecretKey)
	assert.Equal(t, "billing-db", cfg.Database.Host)
	assert.Equal(t, "http://localstack:4566/000000000000/billing-inbox.fifo", cfg.Messaging.InboxQueueURL)
	assert.Equal(t, time.Second, cfg.Messaging.PollWait)
	assert.Equal(t, 2*time.Second, cfg.Messaging.OutboxPollInterval)
	assert.Equal(t, "http://mpfake:8095", cfg.MercadoPago.BaseURL)
	assert.Equal(t, "TEST-token", cfg.MercadoPago.AccessToken)
	assert.Equal(t, "LOJA2POS01", cfg.MercadoPago.POSExternalID)
	assert.Equal(t, 30*time.Second, cfg.MercadoPago.PaymentExpiration)
	assert.Equal(t, "ses", cfg.Email.Provider)
	assert.Equal(t, "oficina@example.com", cfg.Email.SESSenderEmail)
	assert.Equal(t, "contato@example.com", cfg.Email.SESReplyTo)
	assert.Equal(t, "oficina", cfg.Email.SESConfigSet)
}
