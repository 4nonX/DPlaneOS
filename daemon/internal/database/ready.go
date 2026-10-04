package database

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"
)

// WaitReady pings the database until it answers or timeout elapses.
//
// PostgreSQL accepting connections is not the same as the dplaneos role and
// database existing: on NixOS those are created by postgresql-setup.service
// after postgresql.service is already up. Retrying here keeps a startup-order
// race from turning into a failed unit.
func WaitReady(db *sql.DB, timeout, interval time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for attempt := 1; ; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), interval)
		lastErr = db.PingContext(ctx)
		cancel()
		if lastErr == nil {
			if attempt > 1 {
				log.Printf("DB: ready after %d attempts", attempt)
			}
			return nil
		}
		if time.Now().Add(interval).After(deadline) {
			return fmt.Errorf("database not ready after %s: %w", timeout, lastErr)
		}
		if attempt == 1 || attempt%5 == 0 {
			log.Printf("DB: not ready yet (attempt %d): %v", attempt, lastErr)
		}
		time.Sleep(interval)
	}
}
