// URI resolution and connection testing for each database type.
package services

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"datavault/models"
)

// ResolveURI builds the final connection URI for a source,
// merging profile credentials when connection_mode == "profile".
func ResolveURI(src *models.DatabaseSource, profile *models.CredentialProfile) (string, error) {
	if src.ConnectionMode == "uri" {
		return src.URI, nil
	}
	// profile mode — build URI from host/port + profile credentials
	host := src.Host
	port := 0
	if src.Port != nil {
		port = *src.Port
	}
	user := ""
	pass := ""
	if profile != nil {
		user = profile.Username
		pass = profile.Password
	}

	switch src.DBType {
	case models.DBRedis:
		if port == 0 {
			port = 6379
		}
		if user != "" && pass != "" {
			return fmt.Sprintf("redis://%s:%s@%s:%d", url.QueryEscape(user), url.QueryEscape(pass), host, port), nil
		}
		return fmt.Sprintf("redis://%s:%d", host, port), nil

	case models.DBMongoDB:
		if port == 0 {
			port = 27017
		}
		if user != "" {
			return fmt.Sprintf("mongodb://%s:%s@%s:%d", url.QueryEscape(user), url.QueryEscape(pass), host, port), nil
		}
		return fmt.Sprintf("mongodb://%s:%d", host, port), nil

	case models.DBPostgres:
		if port == 0 {
			port = 5432
		}
		db := src.TargetDatabase
		if db == "" {
			db = "postgres"
		}
		if user != "" {
			return fmt.Sprintf("postgresql://%s:%s@%s:%d/%s", url.QueryEscape(user), url.QueryEscape(pass), host, port, db), nil
		}
		return fmt.Sprintf("postgresql://%s:%d/%s", host, port, db), nil

	case models.DBMySQL:
		if port == 0 {
			port = 3306
		}
		db := src.TargetDatabase
		if user != "" {
			return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?parseTime=true", user, pass, host, port, db), nil
		}
		return fmt.Sprintf("tcp(%s:%s)/%s?parseTime=true", host, strconv.Itoa(port), db), nil
	}
	return "", fmt.Errorf("unknown db_type: %s", src.DBType)
}

// TestConnection attempts a lightweight connection to verify reachability.
func TestConnection(src *models.DatabaseSource, profile *models.CredentialProfile) (string, error) {
	uri, err := ResolveURI(src, profile)
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	switch src.DBType {
	case models.DBRedis:
		return testRedis(ctx, uri)
	case models.DBMongoDB:
		return testMongo(ctx, uri)
	case models.DBPostgres:
		return testPostgres(ctx, uri)
	case models.DBMySQL:
		return testMySQL(uri)
	}
	return "", fmt.Errorf("unknown db_type")
}

func testRedis(ctx context.Context, uri string) (string, error) {
	rdb, err := newRedisClient(uri)
	if err != nil {
		return "", err
	}
	defer rdb.Close()
	info, err := rdb.Info(ctx, "server").Result()
	if err != nil {
		return "", err
	}
	ver := extractRedisVersion(info)
	return fmt.Sprintf("Redis %s connected", ver), nil
}

func testMongo(ctx context.Context, uri string) (string, error) {
	client, err := newMongoClient(ctx, uri)
	if err != nil {
		return "", err
	}
	defer client.Disconnect(ctx)
	if err := client.Ping(ctx, nil); err != nil {
		return "", err
	}
	return "MongoDB connected", nil
}

func testPostgres(ctx context.Context, uri string) (string, error) {
	conn, err := newPgConn(ctx, uri)
	if err != nil {
		return "", err
	}
	defer conn.Close(ctx)
	var ver string
	if err := conn.QueryRow(ctx, "SELECT version()").Scan(&ver); err != nil {
		return "", err
	}
	return ver, nil
}

func testMySQL(uri string) (string, error) {
	db, err := newMySQLDB(uri)
	if err != nil {
		return "", err
	}
	defer db.Close()
	var ver string
	if err := db.QueryRow("SELECT VERSION()").Scan(&ver); err != nil {
		return "", err
	}
	return fmt.Sprintf("MySQL %s connected", ver), nil
}
