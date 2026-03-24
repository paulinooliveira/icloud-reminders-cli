package cmd

import (
	"database/sql"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"icloud-reminders/internal/store"
)

type mutationOutcome struct {
	Backend          map[string]interface{}
	Projection       any
	CloudID          string
	AppleID          string
	ListID           string
	Title            string
	DeleteProjection bool
}

func executeMutation(kind, targetType, targetKey string, payload any, useLock bool, exec func() (mutationOutcome, error)) error {
	db, err := store.Open()
	if err != nil {
		return err
	}
	defer db.Close()

	desiredJSON, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	op := store.Operation{
		ID:          uuid.NewString(),
		Kind:        kind,
		TargetType:  targetType,
		DesiredJSON: string(desiredJSON),
		Status:      "pending",
	}
	if targetKey == "" {
		targetKey = "pending:" + op.ID
	}
	op.TargetKey = targetKey
	if err := store.ExecTx(db, func(tx *sql.Tx) error {
		return store.InsertOperation(tx, op)
	}); err != nil {
		return err
	}

	return runRecordedOperation(db, &op, useLock, exec)
}

func runRecordedOperation(db *sql.DB, op *store.Operation, useLock bool, exec func() (mutationOutcome, error)) error {
	if op == nil {
		return nil
	}

	run := func() error {
		op.AttemptCount++
		op.Status = "applying"
		if err := store.UpdateOperation(db, *op); err != nil {
			return err
		}

		outcome, runErr := exec()
		if runErr != nil {
			op.Status = "failed"
			op.ErrorText = runErr.Error()
			return joinMutationErrors(runErr, store.UpdateOperation(db, *op))
		}

		op.AppliedAt = time.Now().UTC().Format(time.RFC3339)
		op.VerifiedAt = op.AppliedAt
		op.Status = "verified"
		op.ErrorText = ""
		if len(outcome.Backend) > 0 {
			backendJSON, err := json.Marshal(outcome.Backend)
			if err != nil {
				return err
			}
			op.BackendJSON = string(backendJSON)
		}
		if outcome.DeleteProjection {
			if err := store.DeleteTargetProjection(db, op.TargetType, op.TargetKey); err != nil {
				return err
			}
		} else if outcome.Projection != nil {
			projectionJSON, err := json.Marshal(outcome.Projection)
			if err != nil {
				return err
			}
			if err := store.SaveTargetProjection(db, op.TargetType, op.TargetKey, outcome.CloudID, outcome.AppleID, outcome.ListID, outcome.Title, string(projectionJSON)); err != nil {
				return err
			}
		}
		if err := store.UpdateOperation(db, *op); err != nil {
			return err
		}
		return pruneHistoricalOperationsForTarget(db, op.TargetType, op.TargetKey)
	}

	if useLock {
		return store.WithMutationLock(run)
	}
	return run()
}

func joinMutationErrors(primary, secondary error) error {
	if primary == nil {
		return secondary
	}
	if secondary == nil {
		return primary
	}
	return secondary
}
