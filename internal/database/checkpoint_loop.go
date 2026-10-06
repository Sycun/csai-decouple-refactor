package database

import (
	"database/sql"
	"strings"
	"time"

	"go.uber.org/zap"
)

// passiveCheckpointLoop runs PRAGMA wal_checkpoint(PASSIVE) on a timer so WAL frames are recycled
// between the sqlite3 driver's own auto-checkpoints. It used to be three fields and two methods on
// the connection wrapper; it is its own type now because a checkpoint schedule is not a database
// handle's job - the wrapper only carries the loop so Close can stop it.
type passiveCheckpointLoop struct {
	db     *sql.DB
	logger *zap.Logger
	name   string
	stop   chan struct{}
	done   chan struct{}
}

// startPassiveCheckpointLoop starts the loop and returns it, or nil when the interval is disabled or
// there is no connection to check point.
func startPassiveCheckpointLoop(db *sql.DB, logger *zap.Logger, name string) *passiveCheckpointLoop {
	if sqlitePassiveCheckpointInterval <= 0 || db == nil {
		return nil
	}
	loop := &passiveCheckpointLoop{
		db:     db,
		logger: logger,
		name:   strings.TrimSpace(name),
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}

	go func() {
		defer close(loop.done)
		ticker := time.NewTicker(sqlitePassiveCheckpointInterval)
		defer ticker.Stop()

		// 启动后先尝试一次，尽快回收已有 WAL 堆积。
		loop.run("startup")
		for {
			select {
			case <-loop.stop:
				return
			case <-ticker.C:
				loop.run("ticker")
			}
		}
	}()
	return loop
}

// run executes one PRAGMA wal_checkpoint(PASSIVE).
func (l *passiveCheckpointLoop) run(trigger string) {
	if l == nil || l.db == nil {
		return
	}
	startAt := time.Now()
	var busy, logFrames, checkpointed int
	err := l.db.QueryRow("PRAGMA wal_checkpoint(PASSIVE)").Scan(&busy, &logFrames, &checkpointed)
	if l.logger == nil {
		return
	}
	fields := []zap.Field{
		zap.String("db", l.name),
		zap.String("trigger", trigger),
		zap.Int("busy", busy),
		zap.Int("log_frames", logFrames),
		zap.Int("checkpointed_frames", checkpointed),
		zap.Int64("elapsed_ms", time.Since(startAt).Milliseconds()),
	}
	if err != nil {
		l.logger.Warn("SQLite PASSIVE checkpoint 完成（失败）",
			append(fields, zap.Error(err))...,
		)
		return
	}
	if busy > 0 {
		l.logger.Debug("SQLite PASSIVE checkpoint 完成（部分推进）", fields...)
		return
	}
	l.logger.Debug("SQLite PASSIVE checkpoint 完成（成功）", fields...)
}

// Stop ends the loop and waits for the goroutine to leave.
func (l *passiveCheckpointLoop) Stop() {
	if l == nil {
		return
	}
	close(l.stop)
	<-l.done
}
