// Package storageops provides transactional safeguards for destructive ZFS
// operations. Every operation (disk wipe, pool create, vdev add, disk replace,
// dataset create) inserts a "pending" row before executing and transitions to
// "committed" or "failed" on completion.
//
// Pre-flight gate: Begin rejects a new operation when a previous operation on
// the same target is already pending, preventing concurrent destructive writes
// on the same device or pool. Operations pending for more than 5 minutes are
// considered stuck and receive a distinct error message.
package storageops

import (
	"database/sql"
	"fmt"
	"time"
)

const stalePendingThreshold = 5 * time.Minute

// OpType identifies the kind of storage operation being tracked.
type OpType string

const (
	OpWipeDisk      OpType = "wipe_disk"
	OpPoolCreate    OpType = "pool_create"
	OpVdevAdd       OpType = "vdev_add"
	OpReplace       OpType = "replace"
	OpAttach        OpType = "attach"
	OpDetach        OpType = "detach"
	OpRemoveDev     OpType = "remove_device"
	OpDatasetCreate OpType = "dataset_create"
)

// OpState is the lifecycle state of a storage operation.
type OpState string

const (
	StatePending   OpState = "pending"
	StateCommitted OpState = "committed"
	StateFailed    OpState = "failed"
)

// Record is a row from storage_operations.
type Record struct {
	ID            int64      `json:"id"`
	OperationType OpType     `json:"operation_type"`
	Target        string     `json:"target"`
	State         OpState    `json:"state"`
	Error         string     `json:"error,omitempty"`
	StartedAt     time.Time  `json:"started_at"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
}

// Begin performs a pre-flight check then inserts a pending row.
//
// If any operation on the same target is already pending, Begin returns an
// error. Operations that have been pending for longer than stalePendingThreshold
// receive a "possible stuck operation" message so operators know to inspect.
//
// On success the returned int64 is the operation ID that must be passed to
// Commit or Fail when the operation completes.
func Begin(db *sql.DB, opType OpType, target string) (int64, error) {
	var existingID int64
	var startedAt time.Time
	err := db.QueryRow(`
		SELECT id, started_at
		FROM storage_operations
		WHERE target = $1 AND state = 'pending'
		ORDER BY started_at DESC
		LIMIT 1`,
		target,
	).Scan(&existingID, &startedAt)
	if err == nil {
		age := time.Since(startedAt).Round(time.Second)
		if age > stalePendingThreshold {
			return 0, fmt.Errorf(
				"operation blocked: operation %d on %q has been pending for %s (possible stuck operation) - clear it via DELETE /api/storage/operations/%d",
				existingID, target, age, existingID,
			)
		}
		return 0, fmt.Errorf(
			"operation blocked: operation %d on %q is already in progress",
			existingID, target,
		)
	}

	var id int64
	if err := db.QueryRow(`
		INSERT INTO storage_operations (operation_type, target, state, started_at)
		VALUES ($1, $2, 'pending', NOW())
		RETURNING id`,
		string(opType), target,
	).Scan(&id); err != nil {
		return 0, fmt.Errorf("storageops: insert: %w", err)
	}
	return id, nil
}

// Commit marks the operation as successfully completed.
func Commit(db *sql.DB, id int64) {
	db.Exec(`UPDATE storage_operations SET state='committed', completed_at=NOW() WHERE id=$1`, id)
}

// Fail marks the operation as failed with an error message.
func Fail(db *sql.DB, id int64, errMsg string) {
	db.Exec(`UPDATE storage_operations SET state='failed', error=$1, completed_at=NOW() WHERE id=$2`, errMsg, id)
}

// ClearStuck transitions a stuck pending operation to failed so a new
// operation can be started on the same target. It returns an error if the
// operation is not pending or does not exist.
func ClearStuck(db *sql.DB, id int64) error {
	res, err := db.Exec(`
		UPDATE storage_operations
		SET state='failed', error='manually cleared by operator', completed_at=NOW()
		WHERE id=$1 AND state='pending'`,
		id,
	)
	if err != nil {
		return fmt.Errorf("storageops: clear: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("operation %d is not pending or does not exist", id)
	}
	return nil
}

// ListRecent returns the most recent storage operations up to limit rows.
func ListRecent(db *sql.DB, limit int) ([]Record, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := db.Query(`
		SELECT id, operation_type, target, state, COALESCE(error,''), started_at, completed_at
		FROM storage_operations
		ORDER BY started_at DESC
		LIMIT $1`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Record
	for rows.Next() {
		var rec Record
		var errStr string
		if err := rows.Scan(
			&rec.ID, &rec.OperationType, &rec.Target,
			&rec.State, &errStr, &rec.StartedAt, &rec.CompletedAt,
		); err != nil {
			continue
		}
		rec.Error = errStr
		out = append(out, rec)
	}
	return out, rows.Err()
}
