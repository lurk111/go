package monitor

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var migrations embed.FS

func OpenStore(dsn string) (*gorm.DB, error) {
	return gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
}
func Migrate(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		if e := tx.Exec("SELECT pg_advisory_xact_lock(713260929)").Error; e != nil {
			return e
		}
		if e := tx.Exec("CREATE TABLE IF NOT EXISTS bid_mon_schema(version integer primary key, applied_at timestamptz NOT NULL)").Error; e != nil {
			return e
		}
		files, err := migrations.ReadDir("migrations")
		if err != nil {
			return err
		}
		sort.Slice(files, func(i, j int) bool { return files[i].Name() < files[j].Name() })
		for _, file := range files {
			if file.IsDir() || !strings.HasSuffix(file.Name(), ".sql") {
				continue
			}
			version, err := strconv.Atoi(strings.TrimSuffix(file.Name(), ".sql"))
			if err != nil {
				return fmt.Errorf("invalid migration filename")
			}
			var n int64
			if err = tx.Table("bid_mon_schema").Where("version=?", version).Count(&n).Error; err != nil {
				return err
			}
			if n > 0 {
				continue
			}
			raw, err := migrations.ReadFile("migrations/" + file.Name())
			if err != nil {
				return err
			}
			// The pgx simple protocol executes this complete, trusted migration.
			// Do not split SQL on semicolons: strings and function bodies may contain them.
			if err = tx.Exec(string(raw)).Error; err != nil {
				return fmt.Errorf("migration %d failed: %w", version, err)
			}
			if err = tx.Exec("INSERT INTO bid_mon_schema VALUES (?,?)", version, time.Now().UTC()).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
func LockLeader(ctx context.Context, db *gorm.DB) (*sql.Conn, error) {
	pool, e := db.DB()
	if e != nil {
		return nil, e
	}
	conn, e := pool.Conn(ctx)
	if e != nil {
		return nil, e
	}
	var ok bool
	e = conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock(713260927)").Scan(&ok)
	if e != nil || !ok {
		conn.Close()
		return nil, fmt.Errorf("another monitor is active")
	}
	return conn, nil
}
func scoped(db *gorm.DB, c Config) *gorm.DB {
	if c.Runtime != nil {
		return db
	}
	return db.Where("task_key IN (?)", db.Session(&gorm.Session{NewDB: true}).Model(&Snapshot{}).Select("key").Where("source=? AND environment=?", c.Source, c.Environment))
}
func eventScope(db *gorm.DB, c Config) *gorm.DB {
	return scoped(db.Model(&Event{}), c).Select("event_id")
}
