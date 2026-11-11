package database

import (
	"testing"

	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildPoolConfig(t *testing.T) {
	cfg, err := buildPoolConfig(&config.DatabaseConfig{
		User: "billing", Password: "secret", Host: "db", Port: "5432", Name: "billing",
	})
	require.NoError(t, err)
	assert.Equal(t, defaultMaxConnectionsPerReplica, cfg.MaxConns)
	assert.Equal(t, "db", cfg.ConnConfig.Host)

	cfg, err = buildPoolConfig(&config.DatabaseConfig{
		User: "billing", Password: "secret", Host: "db", Port: "5432", Name: "billing", MaxConnections: 9,
	})
	require.NoError(t, err)
	assert.Equal(t, int32(9), cfg.MaxConns)
}

func TestBuildPoolConfigRejectsInvalidPort(t *testing.T) {
	_, err := buildPoolConfig(&config.DatabaseConfig{Host: "db", Port: "not-a-port"})
	assert.Error(t, err)
}

func TestNewConnectionFailsWithoutDatabase(t *testing.T) {
	_, err := NewConnection(&config.DatabaseConfig{
		User: "x", Password: "x", Host: "127.0.0.1", Port: "1", Name: "x",
	})
	assert.Error(t, err)
}
