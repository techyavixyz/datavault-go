// Shared DB client constructors used by backup, restore, and db services.
package services

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	goredis "github.com/redis/go-redis/v9"
	"go.mongodb.org/mongo-driver/mongo"
	mongoopts "go.mongodb.org/mongo-driver/mongo/options"
	"github.com/jackc/pgx/v5"
	_ "github.com/go-sql-driver/mysql"
)

func newRedisClient(uri string) (*goredis.Client, error) {
	opt, err := goredis.ParseURL(uri)
	if err != nil {
		return nil, fmt.Errorf("redis URL parse: %w", err)
	}
	return goredis.NewClient(opt), nil
}

// parseRedisURI returns the addr (host:port), username, and password from a redis URI.
func parseRedisURI(uri string) (addr, username, password string, err error) {
	opt, err := goredis.ParseURL(uri)
	if err != nil {
		return "", "", "", fmt.Errorf("redis URL parse: %w", err)
	}
	return opt.Addr, opt.Username, opt.Password, nil
}

func newMongoClient(ctx context.Context, uri string) (*mongo.Client, error) {
	opts := mongoopts.Client().ApplyURI(uri).SetServerSelectionTimeout(10_000_000_000)
	return mongo.Connect(ctx, opts)
}

func newPgConn(ctx context.Context, uri string) (*pgx.Conn, error) {
	return pgx.Connect(ctx, uri)
}

func newMySQLDB(uri string) (*sql.DB, error) {
	// Convert postgresql-style URI to DSN if needed; mysql handler always passes DSN
	return sql.Open("mysql", uri)
}

// extractRedisVersion parses "redis_version:7.2.0" from INFO output.
func extractRedisVersion(info string) string {
	for _, line := range strings.Split(info, "\n") {
		if strings.HasPrefix(line, "redis_version:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "redis_version:"))
		}
	}
	return "unknown"
}
