// Shared DB client constructors used by backup, restore, and db services.
package services

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"go.mongodb.org/mongo-driver/mongo"
	mongoopts "go.mongodb.org/mongo-driver/mongo/options"
	"github.com/jackc/pgx/v5"
	_ "github.com/go-sql-driver/mysql"
)

// newRedisClient creates a Redis client from a URI.
// Supports standard redis:// URIs and the custom redis-sentinel:// scheme.
func newRedisClient(uri string) (*goredis.Client, error) {
	if strings.HasPrefix(uri, "redis-sentinel://") {
		return newRedisSentinelClientFromURI(uri)
	}
	opt, err := goredis.ParseURL(uri)
	if err != nil {
		return nil, fmt.Errorf("redis URL parse: %w", err)
	}
	return goredis.NewClient(opt), nil
}

// newRedisSentinelClientFromURI parses a redis-sentinel:// URI and creates a
// FailoverClient that queries sentinels to discover the current master.
//
// URI format: redis-sentinel://[redisPass[:sentinelPass]@]addr1:port,addr2:port/masterName
func newRedisSentinelClientFromURI(uri string) (*goredis.Client, error) {
	masterName, addrs, redisPass, sentinelPass, err := parseSentinelURI(uri)
	if err != nil {
		return nil, err
	}
	return goredis.NewFailoverClient(&goredis.FailoverOptions{
		MasterName:       masterName,
		SentinelAddrs:    addrs,
		Password:         redisPass,
		SentinelPassword: sentinelPass,
	}), nil
}

// parseSentinelURI decodes a redis-sentinel:// URI into its components.
func parseSentinelURI(uri string) (masterName string, addrs []string, redisPass, sentinelPass string, err error) {
	rest := strings.TrimPrefix(uri, "redis-sentinel://")

	// Extract optional userinfo (everything before the first '@')
	if atIdx := strings.Index(rest, "@"); atIdx != -1 {
		userinfo := rest[:atIdx]
		rest = rest[atIdx+1:]
		if colonIdx := strings.Index(userinfo, ":"); colonIdx != -1 {
			redisPass, _ = url.QueryUnescape(userinfo[:colonIdx])
			sentinelPass, _ = url.QueryUnescape(userinfo[colonIdx+1:])
		} else {
			redisPass, _ = url.QueryUnescape(userinfo)
		}
	}

	// masterName is the last path component
	if slashIdx := strings.LastIndex(rest, "/"); slashIdx != -1 {
		masterName = rest[slashIdx+1:]
		rest = rest[:slashIdx]
	}

	if masterName == "" {
		err = fmt.Errorf("sentinel URI missing master name (format: redis-sentinel://[pass@]addr1,addr2/masterName)")
		return
	}
	if rest == "" {
		err = fmt.Errorf("sentinel URI missing sentinel addresses")
		return
	}
	addrs = strings.Split(rest, ",")
	return
}

// parseRedisURI returns the addr (host:port), username, and password from a redis:// URI.
// For redis-sentinel:// URIs it queries the sentinel cluster to resolve the current master
// and returns that direct address, enabling PSYNC/RDB snapshot mode.
func parseRedisURI(uri string) (addr, username, password string, err error) {
	if strings.HasPrefix(uri, "redis-sentinel://") {
		masterName, addrs, redisPass, sentinelPass, parseErr := parseSentinelURI(uri)
		if parseErr != nil {
			return "", "", "", parseErr
		}
		masterAddr, resolveErr := resolveSentinelMasterAddr(addrs, masterName, sentinelPass)
		if resolveErr != nil {
			return "", "", "", fmt.Errorf("resolve sentinel master: %w", resolveErr)
		}
		return masterAddr, "", redisPass, nil
	}
	opt, err := goredis.ParseURL(uri)
	if err != nil {
		return "", "", "", fmt.Errorf("redis URL parse: %w", err)
	}
	return opt.Addr, opt.Username, opt.Password, nil
}

// resolveSentinelMasterAddr queries each sentinel in turn until one returns the master address.
func resolveSentinelMasterAddr(addrs []string, masterName, sentinelPass string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var lastErr error
	for _, addr := range addrs {
		sc := goredis.NewSentinelClient(&goredis.Options{
			Addr:     addr,
			Password: sentinelPass,
		})
		parts, err := sc.GetMasterAddrByName(ctx, masterName).Result()
		sc.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if len(parts) != 2 {
			lastErr = fmt.Errorf("sentinel %s: unexpected response length %d", addr, len(parts))
			continue
		}
		return net.JoinHostPort(parts[0], parts[1]), nil
	}
	if lastErr != nil {
		return "", fmt.Errorf("no sentinel responded with master address: %w", lastErr)
	}
	return "", fmt.Errorf("no sentinel addresses to query")
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
