package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadAppliesDefaults(t *testing.T) {
	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, "8080", cfg.Server.Port)
	assert.Equal(t, int32(5), cfg.Database.MaxConnections)
	assert.Equal(t, "sa-east-1", cfg.Messaging.Region)
	assert.Equal(t, 20*time.Second, cfg.Messaging.PollWait)
	assert.Equal(t, "https://api.mercadopago.com", cfg.MercadoPago.BaseURL)
}

func TestLoadReadsEnvironment(t *testing.T) {
	t.Setenv("SERVER_PORT", "9090")
	t.Setenv("DATABASE_HOST", "billing-db")
	t.Setenv("MESSAGING_INBOX_QUEUE_URL", "http://localstack:4566/000000000000/billing-inbox.fifo")
	t.Setenv("MESSAGING_POLL_WAIT", "1s")
	t.Setenv("MERCADOPAGO_ACCESS_TOKEN", "TEST-token")

	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, "9090", cfg.Server.Port)
	assert.Equal(t, "billing-db", cfg.Database.Host)
	assert.Equal(t, "http://localstack:4566/000000000000/billing-inbox.fifo", cfg.Messaging.InboxQueueURL)
	assert.Equal(t, time.Second, cfg.Messaging.PollWait)
	assert.Equal(t, "TEST-token", cfg.MercadoPago.AccessToken)
}
