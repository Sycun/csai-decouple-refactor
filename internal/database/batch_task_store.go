package database

import (
	"time"

	"cyberstrike-ai/internal/store"
)

// The batch run ledger SQL lives in store.BatchTasks. What is left here is one line of delegation per
// method the manager and the handlers still call through database.BatchTaskStore: no default, no
// logging, no retry. Removing them is the next cut, once each consumer holds the store itself.

// NewBatchTasks answers a nil store for a nil connection, the way database.Narrow answers a nil
// interface: a consumer built without a database must be able to tell it has none, and every method
// on store.BatchTasks opens with an `s == nil` guard, so the nil pointer refuses instead of panicking.
func NewBatchTasks(db *DB) *store.BatchTasks {
	if db == nil {
		return nil
	}
	return store.NewBatchTasks(db.DB)
}

func (db *DB) CreateBatchQueue(queueID string, title string, role string, agentMode string, scheduleMode string, cronExpr string, nextRunAt *time.Time, projectID string, concurrency int, tasks []map[string]interface{}, hitlPolicies ...string) error {
	return NewBatchTasks(db).CreateBatchQueue(queueID, title, role, agentMode, scheduleMode, cronExpr, nextRunAt, projectID, concurrency, tasks, hitlPolicies...)
}

func (db *DB) GetBatchQueue(queueID string) (*store.BatchTaskQueueRow, error) {
	return NewBatchTasks(db).GetBatchQueue(queueID)
}

func (db *DB) GetAllBatchQueues() ([]*store.BatchTaskQueueRow, error) {
	return NewBatchTasks(db).GetAllBatchQueues()
}

func (db *DB) ListBatchQueuesForAccess(limit int, offset int, status string, keyword string, userID string, scope string) ([]*store.BatchTaskQueueRow, error) {
	return NewBatchTasks(db).ListBatchQueuesForAccess(limit, offset, status, keyword, userID, scope)
}

func (db *DB) CountBatchQueuesForAccess(status string, keyword string, userID string, scope string) (int, error) {
	return NewBatchTasks(db).CountBatchQueuesForAccess(status, keyword, userID, scope)
}

func (db *DB) GetBatchTasks(queueID string) ([]*store.BatchTaskRow, error) {
	return NewBatchTasks(db).GetBatchTasks(queueID)
}

func (db *DB) UpdateBatchQueueStatus(queueID string, status string) error {
	return NewBatchTasks(db).UpdateBatchQueueStatus(queueID, status)
}

func (db *DB) UpdateBatchTaskStatus(queueID string, taskID string, status string, conversationID string, result string, errorMsg string) error {
	return NewBatchTasks(db).UpdateBatchTaskStatus(queueID, taskID, status, conversationID, result, errorMsg)
}

func (db *DB) UpdateBatchQueueCurrentIndex(queueID string, currentIndex int) error {
	return NewBatchTasks(db).UpdateBatchQueueCurrentIndex(queueID, currentIndex)
}

func (db *DB) UpdateBatchQueueMetadata(queueID string, title string, role string, agentMode string, concurrency int, hitlPolicies ...string) error {
	return NewBatchTasks(db).UpdateBatchQueueMetadata(queueID, title, role, agentMode, concurrency, hitlPolicies...)
}

func (db *DB) UpdateBatchQueueSchedule(queueID string, scheduleMode string, cronExpr string, nextRunAt *time.Time) error {
	return NewBatchTasks(db).UpdateBatchQueueSchedule(queueID, scheduleMode, cronExpr, nextRunAt)
}

func (db *DB) UpdateBatchQueueScheduleEnabled(queueID string, enabled bool) error {
	return NewBatchTasks(db).UpdateBatchQueueScheduleEnabled(queueID, enabled)
}

func (db *DB) RecordBatchQueueScheduledTriggerStart(queueID string, at time.Time) error {
	return NewBatchTasks(db).RecordBatchQueueScheduledTriggerStart(queueID, at)
}

func (db *DB) SetBatchQueueLastScheduleError(queueID string, msg string) error {
	return NewBatchTasks(db).SetBatchQueueLastScheduleError(queueID, msg)
}

func (db *DB) SetBatchQueueLastRunError(queueID string, msg string) error {
	return NewBatchTasks(db).SetBatchQueueLastRunError(queueID, msg)
}

func (db *DB) ResetBatchQueueForRerun(queueID string) error {
	return NewBatchTasks(db).ResetBatchQueueForRerun(queueID)
}

func (db *DB) UpdateBatchTaskMessage(queueID string, taskID string, message string) error {
	return NewBatchTasks(db).UpdateBatchTaskMessage(queueID, taskID, message)
}

func (db *DB) AddBatchTask(queueID string, taskID string, message string) error {
	return NewBatchTasks(db).AddBatchTask(queueID, taskID, message)
}

func (db *DB) CancelPendingBatchTasks(queueID string, completedAt time.Time) error {
	return NewBatchTasks(db).CancelPendingBatchTasks(queueID, completedAt)
}

func (db *DB) PrepareBatchSingleTaskRun(queueID string, taskID string, taskIndex int, resetTask bool, resumeQueue bool) error {
	return NewBatchTasks(db).PrepareBatchSingleTaskRun(queueID, taskID, taskIndex, resetTask, resumeQueue)
}

func (db *DB) DeleteBatchTask(queueID string, taskID string) error {
	return NewBatchTasks(db).DeleteBatchTask(queueID, taskID)
}

func (db *DB) DeleteBatchQueue(queueID string) error {
	return NewBatchTasks(db).DeleteBatchQueue(queueID)
}
