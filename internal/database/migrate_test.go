package database

import (
	"context"
	"os"
	"testing"

	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/config"
	"github.com/stretchr/testify/require"
)

func TestRunMigrationsAgainstPostgres(t *testing.T) {
	if os.Getenv("DATABASE_HOST") == "" {
		t.Skip("DATABASE_HOST nao definido")
	}

	pool, err := NewConnection(&config.DatabaseConfig{
		User:     os.Getenv("DATABASE_USER"),
		Password: os.Getenv("DATABASE_PASSWORD"),
		Host:     os.Getenv("DATABASE_HOST"),
		Port:     os.Getenv("DATABASE_PORT"),
		Name:     os.Getenv("DATABASE_NAME"),
	})
	require.NoError(t, err)
	defer pool.Close()

	require.NoError(t, RunMigrations(pool))
	require.NoError(t, RunMigrations(pool))

	var tables int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT count(*) FROM information_schema.tables
		 WHERE table_schema = 'public'
		   AND table_name IN ('quotes', 'quote_items', 'payments', 'outbox', 'processed_messages')`,
	).Scan(&tables))
	require.Equal(t, 5, tables)
}
