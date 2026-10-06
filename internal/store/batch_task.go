package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"cyberstrike-ai/internal/sqltime"
)

// BatchTasks owns the batch run ledger: the queue row that carries schedule, concurrency and the
// last error an operator saw, and the per-task rows that carry one message each. The two tables are
// one domain - a queue is only ever read together with its tasks - so they move together, with their
// schema.
//
// The reads that reach into rbac_resource_assignments and projects stay reads; what this package
// claims is the writes to batch_task_queues and batch_tasks.
//
// Three log lines came over with the SQL and did not: each one warned that a stored created_at
// could not be parsed before falling back to time.Now(). The fallback is kept exactly (a store test
// pins it); the warning is the documented cost of a package that holds no logger.
type BatchTasks struct {
	db *sql.DB
}

func NewBatchTasks(db *sql.DB) *BatchTasks { return &BatchTasks{db: db} }

func (s *BatchTasks) requireDB() error {
	if s == nil || s.db == nil {
		return errors.New("store: batch tasks require a database")
	}
	return nil
}

// BatchTaskQueueRow 批量任务队列数据库行
type BatchTaskQueueRow struct {
	ID                    string
	Title                 sql.NullString
	Role                  sql.NullString
	AgentMode             sql.NullString
	HITLPolicy            string
	ScheduleMode          sql.NullString
	CronExpr              sql.NullString
	NextRunAt             sql.NullTime
	ScheduleEnabled       sql.NullInt64
	LastScheduleTriggerAt sql.NullTime
	LastScheduleError     sql.NullString
	LastRunError          sql.NullString
	ProjectID             sql.NullString
	Concurrency           sql.NullInt64
	Status                string
	CreatedAt             time.Time
	StartedAt             sql.NullTime
	CompletedAt           sql.NullTime
	CurrentIndex          int
}

// BatchTaskRow 批量任务数据库行
type BatchTaskRow struct {
	ID             string
	QueueID        string
	Message        string
	ConversationID sql.NullString
	Status         string
	StartedAt      sql.NullTime
	CompletedAt    sql.NullTime
	Error          sql.NullString
	Result         sql.NullString
}

// batchQueuesSchema and batchTasksSchema are the two tables, verbatim from the data layer. The queue
// comes first: batch_tasks.queue_id has a foreign key onto it with ON DELETE CASCADE.
const batchQueuesSchema = `
	CREATE TABLE IF NOT EXISTS batch_task_queues (
		id TEXT PRIMARY KEY,
		title TEXT,
		role TEXT,
		agent_mode TEXT NOT NULL DEFAULT 'eino_single',
		hitl_policy TEXT NOT NULL DEFAULT '',
		schedule_mode TEXT NOT NULL DEFAULT 'manual',
		cron_expr TEXT,
		next_run_at DATETIME,
		schedule_enabled INTEGER NOT NULL DEFAULT 1,
		last_schedule_trigger_at DATETIME,
		last_schedule_error TEXT,
		last_run_error TEXT,
		project_id TEXT,
		concurrency INTEGER NOT NULL DEFAULT 1,
		status TEXT NOT NULL,
		created_at DATETIME NOT NULL,
		started_at DATETIME,
		completed_at DATETIME,
		current_index INTEGER NOT NULL DEFAULT 0
	);`

const batchTasksSchema = `
	CREATE TABLE IF NOT EXISTS batch_tasks (
		id TEXT PRIMARY KEY,
		queue_id TEXT NOT NULL,
		message TEXT NOT NULL,
		conversation_id TEXT,
		status TEXT NOT NULL,
		started_at DATETIME,
		completed_at DATETIME,
		error TEXT,
		result TEXT,
		FOREIGN KEY (queue_id) REFERENCES batch_task_queues(id) ON DELETE CASCADE
	);`

const batchTaskIndexes = `
CREATE INDEX IF NOT EXISTS idx_batch_tasks_queue_id ON batch_tasks(queue_id);
CREATE INDEX IF NOT EXISTS idx_batch_task_queues_created_at ON batch_task_queues(created_at);
CREATE INDEX IF NOT EXISTS idx_batch_task_queues_title ON batch_task_queues(title);`

// EnsureSchema creates the two tables. Indexes are NOT built here: idx_batch_task_queues_title is on
// a column that only exists after migrateQueueColumns, so boot has to run EnsureSchema ->
// MigrateQueueColumns -> EnsureIndexes in that order, exactly the order the connection wrapper used.
func (s *BatchTasks) EnsureSchema() error {
	if err := s.requireDB(); err != nil {
		return err
	}
	if _, err := s.db.Exec(batchQueuesSchema); err != nil {
		return fmt.Errorf("创建batch_task_queues表失败: %w", err)
	}
	if _, err := s.db.Exec(batchTasksSchema); err != nil {
		return fmt.Errorf("创建batch_tasks表失败: %w", err)
	}
	return nil
}

// MigrateQueueColumns backfills the columns added after the first release. A column that will not
// backfill is returned as an error naming that column: the data layer used to log one warning per
// column and continue, and boot now logs this single line instead.
func (s *BatchTasks) MigrateQueueColumns() error {
	if err := s.requireDB(); err != nil {
		return err
	}
	// 检查title字段是否存在
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('batch_task_queues') WHERE name='title'").Scan(&count)
	if err != nil {
		// 如果查询失败，尝试添加字段
		if _, addErr := s.db.Exec("ALTER TABLE batch_task_queues ADD COLUMN title TEXT"); addErr != nil {
			// 如果字段已存在，忽略错误
			errMsg := strings.ToLower(addErr.Error())
			if !strings.Contains(errMsg, "duplicate column") && !strings.Contains(errMsg, "already exists") {

				return fmt.Errorf("补列 batch_task_queues.title 失败: %w", addErr)
			}
		}
	} else if count == 0 {
		// 字段不存在，添加它
		if _, err := s.db.Exec("ALTER TABLE batch_task_queues ADD COLUMN title TEXT"); err != nil {

			return fmt.Errorf("补列 batch_task_queues.title 失败: %w", err)
		}
	}

	// 检查role字段是否存在
	var roleCount int
	err = s.db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('batch_task_queues') WHERE name='role'").Scan(&roleCount)
	if err != nil {
		// 如果查询失败，尝试添加字段
		if _, addErr := s.db.Exec("ALTER TABLE batch_task_queues ADD COLUMN role TEXT"); addErr != nil {
			// 如果字段已存在，忽略错误
			errMsg := strings.ToLower(addErr.Error())
			if !strings.Contains(errMsg, "duplicate column") && !strings.Contains(errMsg, "already exists") {

				return fmt.Errorf("补列 batch_task_queues.role 失败: %w", addErr)
			}
		}
	} else if roleCount == 0 {
		// 字段不存在，添加它
		if _, err := s.db.Exec("ALTER TABLE batch_task_queues ADD COLUMN role TEXT"); err != nil {

			return fmt.Errorf("补列 batch_task_queues.role 失败: %w", err)
		}
	}

	// 检查agent_mode字段是否存在
	var agentModeCount int
	err = s.db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('batch_task_queues') WHERE name='agent_mode'").Scan(&agentModeCount)
	if err != nil {
		if _, addErr := s.db.Exec("ALTER TABLE batch_task_queues ADD COLUMN agent_mode TEXT NOT NULL DEFAULT 'eino_single'"); addErr != nil {
			errMsg := strings.ToLower(addErr.Error())
			if !strings.Contains(errMsg, "duplicate column") && !strings.Contains(errMsg, "already exists") {

				return fmt.Errorf("补列 batch_task_queues.agent_mode 失败: %w", addErr)
			}
		}
	} else if agentModeCount == 0 {
		if _, err := s.db.Exec("ALTER TABLE batch_task_queues ADD COLUMN agent_mode TEXT NOT NULL DEFAULT 'eino_single'"); err != nil {

			return fmt.Errorf("补列 batch_task_queues.agent_mode 失败: %w", err)
		}
	}

	// 检查schedule_mode字段是否存在
	var scheduleModeCount int
	err = s.db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('batch_task_queues') WHERE name='schedule_mode'").Scan(&scheduleModeCount)
	if err != nil {
		if _, addErr := s.db.Exec("ALTER TABLE batch_task_queues ADD COLUMN schedule_mode TEXT NOT NULL DEFAULT 'manual'"); addErr != nil {
			errMsg := strings.ToLower(addErr.Error())
			if !strings.Contains(errMsg, "duplicate column") && !strings.Contains(errMsg, "already exists") {

				return fmt.Errorf("补列 batch_task_queues.schedule_mode 失败: %w", addErr)
			}
		}
	} else if scheduleModeCount == 0 {
		if _, err := s.db.Exec("ALTER TABLE batch_task_queues ADD COLUMN schedule_mode TEXT NOT NULL DEFAULT 'manual'"); err != nil {

			return fmt.Errorf("补列 batch_task_queues.schedule_mode 失败: %w", err)
		}
	}

	// 检查cron_expr字段是否存在
	var cronExprCount int
	err = s.db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('batch_task_queues') WHERE name='cron_expr'").Scan(&cronExprCount)
	if err != nil {
		if _, addErr := s.db.Exec("ALTER TABLE batch_task_queues ADD COLUMN cron_expr TEXT"); addErr != nil {
			errMsg := strings.ToLower(addErr.Error())
			if !strings.Contains(errMsg, "duplicate column") && !strings.Contains(errMsg, "already exists") {

				return fmt.Errorf("补列 batch_task_queues.cron_expr 失败: %w", addErr)
			}
		}
	} else if cronExprCount == 0 {
		if _, err := s.db.Exec("ALTER TABLE batch_task_queues ADD COLUMN cron_expr TEXT"); err != nil {

			return fmt.Errorf("补列 batch_task_queues.cron_expr 失败: %w", err)
		}
	}

	// 检查next_run_at字段是否存在
	var nextRunAtCount int
	err = s.db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('batch_task_queues') WHERE name='next_run_at'").Scan(&nextRunAtCount)
	if err != nil {
		if _, addErr := s.db.Exec("ALTER TABLE batch_task_queues ADD COLUMN next_run_at DATETIME"); addErr != nil {
			errMsg := strings.ToLower(addErr.Error())
			if !strings.Contains(errMsg, "duplicate column") && !strings.Contains(errMsg, "already exists") {

				return fmt.Errorf("补列 batch_task_queues.next_run_at 失败: %w", addErr)
			}
		}
	} else if nextRunAtCount == 0 {
		if _, err := s.db.Exec("ALTER TABLE batch_task_queues ADD COLUMN next_run_at DATETIME"); err != nil {

			return fmt.Errorf("补列 batch_task_queues.next_run_at 失败: %w", err)
		}
	}

	// schedule_enabled：0=暂停 Cron 自动调度，1=允许（手工执行不受影响）
	var scheduleEnCount int
	err = s.db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('batch_task_queues') WHERE name='schedule_enabled'").Scan(&scheduleEnCount)
	if err != nil {
		if _, addErr := s.db.Exec("ALTER TABLE batch_task_queues ADD COLUMN schedule_enabled INTEGER NOT NULL DEFAULT 1"); addErr != nil {
			errMsg := strings.ToLower(addErr.Error())
			if !strings.Contains(errMsg, "duplicate column") && !strings.Contains(errMsg, "already exists") {

				return fmt.Errorf("补列 batch_task_queues.schedule_enabled 失败: %w", addErr)
			}
		}
	} else if scheduleEnCount == 0 {
		if _, err := s.db.Exec("ALTER TABLE batch_task_queues ADD COLUMN schedule_enabled INTEGER NOT NULL DEFAULT 1"); err != nil {

			return fmt.Errorf("补列 batch_task_queues.schedule_enabled 失败: %w", err)
		}
	}

	var lastTrigCount int
	err = s.db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('batch_task_queues') WHERE name='last_schedule_trigger_at'").Scan(&lastTrigCount)
	if err != nil {
		if _, addErr := s.db.Exec("ALTER TABLE batch_task_queues ADD COLUMN last_schedule_trigger_at DATETIME"); addErr != nil {
			errMsg := strings.ToLower(addErr.Error())
			if !strings.Contains(errMsg, "duplicate column") && !strings.Contains(errMsg, "already exists") {

				return fmt.Errorf("补列 batch_task_queues.last_schedule_trigger_at 失败: %w", addErr)
			}
		}
	} else if lastTrigCount == 0 {
		if _, err := s.db.Exec("ALTER TABLE batch_task_queues ADD COLUMN last_schedule_trigger_at DATETIME"); err != nil {

			return fmt.Errorf("补列 batch_task_queues.last_schedule_trigger_at 失败: %w", err)
		}
	}

	var lastSchedErrCount int
	err = s.db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('batch_task_queues') WHERE name='last_schedule_error'").Scan(&lastSchedErrCount)
	if err != nil {
		if _, addErr := s.db.Exec("ALTER TABLE batch_task_queues ADD COLUMN last_schedule_error TEXT"); addErr != nil {
			errMsg := strings.ToLower(addErr.Error())
			if !strings.Contains(errMsg, "duplicate column") && !strings.Contains(errMsg, "already exists") {

				return fmt.Errorf("补列 batch_task_queues.last_schedule_error 失败: %w", addErr)
			}
		}
	} else if lastSchedErrCount == 0 {
		if _, err := s.db.Exec("ALTER TABLE batch_task_queues ADD COLUMN last_schedule_error TEXT"); err != nil {

			return fmt.Errorf("补列 batch_task_queues.last_schedule_error 失败: %w", err)
		}
	}

	var lastRunErrCount int
	err = s.db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('batch_task_queues') WHERE name='last_run_error'").Scan(&lastRunErrCount)
	if err != nil {
		if _, addErr := s.db.Exec("ALTER TABLE batch_task_queues ADD COLUMN last_run_error TEXT"); addErr != nil {
			errMsg := strings.ToLower(addErr.Error())
			if !strings.Contains(errMsg, "duplicate column") && !strings.Contains(errMsg, "already exists") {

				return fmt.Errorf("补列 batch_task_queues.last_run_error 失败: %w", addErr)
			}
		}
	} else if lastRunErrCount == 0 {
		if _, err := s.db.Exec("ALTER TABLE batch_task_queues ADD COLUMN last_run_error TEXT"); err != nil {

			return fmt.Errorf("补列 batch_task_queues.last_run_error 失败: %w", err)
		}
	}

	var projectIDCount int
	err = s.db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('batch_task_queues') WHERE name='project_id'").Scan(&projectIDCount)
	if err != nil {
		if _, addErr := s.db.Exec("ALTER TABLE batch_task_queues ADD COLUMN project_id TEXT"); addErr != nil {
			errMsg := strings.ToLower(addErr.Error())
			if !strings.Contains(errMsg, "duplicate column") && !strings.Contains(errMsg, "already exists") {

				return fmt.Errorf("补列 batch_task_queues.project_id 失败: %w", addErr)
			}
		}
	} else if projectIDCount == 0 {
		if _, err := s.db.Exec("ALTER TABLE batch_task_queues ADD COLUMN project_id TEXT"); err != nil {

			return fmt.Errorf("补列 batch_task_queues.project_id 失败: %w", err)
		}
	}

	var hitlPolicyCount int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('batch_task_queues') WHERE name='hitl_policy'").Scan(&hitlPolicyCount); err != nil {
		return fmt.Errorf("检查队列审批字段失败: %w", err)
	}
	if hitlPolicyCount == 0 {
		if _, err := s.db.Exec("ALTER TABLE batch_task_queues ADD COLUMN hitl_policy TEXT NOT NULL DEFAULT ''"); err != nil {
			return fmt.Errorf("添加队列审批字段失败: %w", err)
		}
	}

	var concurrencyCount int
	err = s.db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('batch_task_queues') WHERE name='concurrency'").Scan(&concurrencyCount)
	if err != nil {
		if _, addErr := s.db.Exec("ALTER TABLE batch_task_queues ADD COLUMN concurrency INTEGER NOT NULL DEFAULT 1"); addErr != nil {
			errMsg := strings.ToLower(addErr.Error())
			if !strings.Contains(errMsg, "duplicate column") && !strings.Contains(errMsg, "already exists") {

				return fmt.Errorf("补列 batch_task_queues.concurrency 失败: %w", addErr)
			}
		}
	} else if concurrencyCount == 0 {
		if _, err := s.db.Exec("ALTER TABLE batch_task_queues ADD COLUMN concurrency INTEGER NOT NULL DEFAULT 1"); err != nil {

			return fmt.Errorf("补列 batch_task_queues.concurrency 失败: %w", err)
		}
	}

	// owner_user_id：RBAC 迁移搬来时它建的列；probe + duplicate 容错同其他列。
	var ownerCount int
	err = s.db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('batch_task_queues') WHERE name='owner_user_id'").Scan(&ownerCount)
	if err != nil || ownerCount == 0 {
		if _, addErr := s.db.Exec("ALTER TABLE batch_task_queues ADD COLUMN owner_user_id TEXT"); addErr != nil {
			errMsg := strings.ToLower(addErr.Error())
			if !strings.Contains(errMsg, "duplicate column") && !strings.Contains(errMsg, "already exists") {
				return fmt.Errorf("补列 batch_task_queues.owner_user_id 失败: %w", addErr)
			}
		}
	}

	return nil
}

// EnsureIndexes builds the three indexes, after any column they need.
func (s *BatchTasks) EnsureIndexes() error {
	if err := s.requireDB(); err != nil {
		return err
	}
	if _, err := s.db.Exec(batchTaskIndexes); err != nil {
		return fmt.Errorf("创建batch_task索引失败: %w", err)
	}
	return nil
}

// CreateBatchQueue 创建批量任务队列
func (s *BatchTasks) CreateBatchQueue(
	queueID string,
	title string,
	role string,
	agentMode string,
	scheduleMode string,
	cronExpr string,
	nextRunAt *time.Time,
	projectID string,
	concurrency int,
	tasks []map[string]interface{},
	hitlPolicies ...string,
) error {
	if err := s.requireDB(); err != nil {
		return err
	}
	policy := ""
	if len(hitlPolicies) > 0 {
		policy = hitlPolicies[0]
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("开始事务失败: %w", err)
	}
	defer tx.Rollback()

	now := time.Now()
	var nextRunAtValue interface{}
	if nextRunAt != nil {
		nextRunAtValue = *nextRunAt
	}

	var projectIDVal interface{}
	if strings.TrimSpace(projectID) != "" {
		projectIDVal = strings.TrimSpace(projectID)
	}
	_, err = tx.Exec(
		"INSERT INTO batch_task_queues (id, title, role, agent_mode, hitl_policy, schedule_mode, cron_expr, next_run_at, schedule_enabled, project_id, concurrency, status, created_at, current_index) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		queueID, title, role, agentMode, policy, scheduleMode, cronExpr, nextRunAtValue, 1, projectIDVal, concurrency, "pending", now, 0,
	)
	if err != nil {
		return fmt.Errorf("创建批量任务队列失败: %w", err)
	}

	// 插入任务
	for _, task := range tasks {
		taskID, ok := task["id"].(string)
		if !ok {
			continue
		}
		message, ok := task["message"].(string)
		if !ok {
			continue
		}

		_, err = tx.Exec(
			"INSERT INTO batch_tasks (id, queue_id, message, status) VALUES (?, ?, ?, ?)",
			taskID, queueID, message, "pending",
		)
		if err != nil {
			return fmt.Errorf("创建批量任务失败: %w", err)
		}
	}

	return tx.Commit()
}

const batchQueueSelectColumns = `id, title, role, agent_mode, hitl_policy, schedule_mode, cron_expr, next_run_at, schedule_enabled, last_schedule_trigger_at, last_schedule_error, last_run_error, project_id, concurrency, status, created_at, started_at, completed_at, current_index`

// GetBatchQueue 获取批量任务队列
func (s *BatchTasks) GetBatchQueue(queueID string) (*BatchTaskQueueRow, error) {
	if err := s.requireDB(); err != nil {
		return nil, err
	}
	var row BatchTaskQueueRow
	var createdAt string
	err := s.db.QueryRow(
		"SELECT "+batchQueueSelectColumns+" FROM batch_task_queues WHERE id = ?",
		queueID,
	).Scan(&row.ID, &row.Title, &row.Role, &row.AgentMode, &row.HITLPolicy, &row.ScheduleMode, &row.CronExpr, &row.NextRunAt, &row.ScheduleEnabled, &row.LastScheduleTriggerAt, &row.LastScheduleError, &row.LastRunError, &row.ProjectID, &row.Concurrency, &row.Status, &createdAt, &row.StartedAt, &row.CompletedAt, &row.CurrentIndex)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("查询批量任务队列失败: %w", err)
	}

	parsedTime, parsed := sqltime.ParseOK(createdAt)
	if !parsed {
		parsedTime = time.Now()
	}
	row.CreatedAt = parsedTime
	return &row, nil
}

// GetAllBatchQueues 获取所有批量任务队列
func (s *BatchTasks) GetAllBatchQueues() ([]*BatchTaskQueueRow, error) {
	if err := s.requireDB(); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(
		"SELECT " + batchQueueSelectColumns + " FROM batch_task_queues ORDER BY created_at DESC",
	)
	if err != nil {
		return nil, fmt.Errorf("查询批量任务队列列表失败: %w", err)
	}
	defer rows.Close()

	var queues []*BatchTaskQueueRow
	for rows.Next() {
		var row BatchTaskQueueRow
		var createdAt string
		if err := rows.Scan(&row.ID, &row.Title, &row.Role, &row.AgentMode, &row.HITLPolicy, &row.ScheduleMode, &row.CronExpr, &row.NextRunAt, &row.ScheduleEnabled, &row.LastScheduleTriggerAt, &row.LastScheduleError, &row.LastRunError, &row.ProjectID, &row.Concurrency, &row.Status, &createdAt, &row.StartedAt, &row.CompletedAt, &row.CurrentIndex); err != nil {
			return nil, fmt.Errorf("扫描批量任务队列失败: %w", err)
		}
		parsedTime, parsed := sqltime.ParseOK(createdAt)
		if !parsed {
			parsedTime = time.Now()
		}
		row.CreatedAt = parsedTime
		queues = append(queues, &row)
	}

	return queues, nil
}

func (s *BatchTasks) ListBatchQueuesForAccess(limit, offset int, status, keyword, userID, scope string) ([]*BatchTaskQueueRow, error) {
	if err := s.requireDB(); err != nil {
		return nil, err
	}
	query := "SELECT " + batchQueueSelectColumns + " FROM batch_task_queues WHERE 1=1"
	args := []interface{}{}

	// 状态筛选
	if status != "" && status != "all" {
		query += " AND status = ?"
		args = append(args, status)
	}

	// 关键字搜索（搜索队列ID和标题）
	if keyword != "" {
		query += " AND (id LIKE ? OR title LIKE ?)"
		args = append(args, "%"+keyword+"%", "%"+keyword+"%")
	}
	userID = strings.TrimSpace(userID)
	if userID != "" && scope != ScopeAll {
		query += ` AND (
			owner_user_id = ?
			OR EXISTS (
				SELECT 1 FROM rbac_resource_assignments ra
				WHERE ra.user_id = ? AND ra.resource_type = 'batch_task' AND ra.resource_id = batch_task_queues.id
			)
			OR (
				project_id IS NOT NULL AND project_id <> '' AND (
					EXISTS (SELECT 1 FROM projects p WHERE p.id = batch_task_queues.project_id AND p.owner_user_id = ?)
					OR EXISTS (
						SELECT 1 FROM rbac_resource_assignments pra
						WHERE pra.user_id = ? AND pra.resource_type = 'project' AND pra.resource_id = batch_task_queues.project_id
					)
				)
			)
		)`
		args = append(args, userID, userID, userID, userID)
	}

	query += " ORDER BY created_at DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("查询批量任务队列列表失败: %w", err)
	}
	defer rows.Close()

	var queues []*BatchTaskQueueRow
	for rows.Next() {
		var row BatchTaskQueueRow
		var createdAt string
		if err := rows.Scan(&row.ID, &row.Title, &row.Role, &row.AgentMode, &row.HITLPolicy, &row.ScheduleMode, &row.CronExpr, &row.NextRunAt, &row.ScheduleEnabled, &row.LastScheduleTriggerAt, &row.LastScheduleError, &row.LastRunError, &row.ProjectID, &row.Concurrency, &row.Status, &createdAt, &row.StartedAt, &row.CompletedAt, &row.CurrentIndex); err != nil {
			return nil, fmt.Errorf("扫描批量任务队列失败: %w", err)
		}
		parsedTime, parsed := sqltime.ParseOK(createdAt)
		if !parsed {
			parsedTime = time.Now()
		}
		row.CreatedAt = parsedTime
		queues = append(queues, &row)
	}

	return queues, nil
}

func (s *BatchTasks) CountBatchQueuesForAccess(status, keyword, userID, scope string) (int, error) {
	if err := s.requireDB(); err != nil {
		return 0, err
	}
	query := "SELECT COUNT(*) FROM batch_task_queues WHERE 1=1"
	args := []interface{}{}

	// 状态筛选
	if status != "" && status != "all" {
		query += " AND status = ?"
		args = append(args, status)
	}

	// 关键字搜索（搜索队列ID和标题）
	if keyword != "" {
		query += " AND (id LIKE ? OR title LIKE ?)"
		args = append(args, "%"+keyword+"%", "%"+keyword+"%")
	}
	userID = strings.TrimSpace(userID)
	if userID != "" && scope != ScopeAll {
		query += ` AND (
			owner_user_id = ?
			OR EXISTS (
				SELECT 1 FROM rbac_resource_assignments ra
				WHERE ra.user_id = ? AND ra.resource_type = 'batch_task' AND ra.resource_id = batch_task_queues.id
			)
			OR (
				project_id IS NOT NULL AND project_id <> '' AND (
					EXISTS (SELECT 1 FROM projects p WHERE p.id = batch_task_queues.project_id AND p.owner_user_id = ?)
					OR EXISTS (
						SELECT 1 FROM rbac_resource_assignments pra
						WHERE pra.user_id = ? AND pra.resource_type = 'project' AND pra.resource_id = batch_task_queues.project_id
					)
				)
			)
		)`
		args = append(args, userID, userID, userID, userID)
	}

	var count int
	err := s.db.QueryRow(query, args...).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("统计批量任务队列总数失败: %w", err)
	}

	return count, nil
}

// GetBatchTasks 获取批量任务队列的所有任务
func (s *BatchTasks) GetBatchTasks(queueID string) ([]*BatchTaskRow, error) {
	if err := s.requireDB(); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(
		"SELECT id, queue_id, message, conversation_id, status, started_at, completed_at, error, result FROM batch_tasks WHERE queue_id = ? ORDER BY rowid ASC",
		queueID,
	)
	if err != nil {
		return nil, fmt.Errorf("查询批量任务失败: %w", err)
	}
	defer rows.Close()

	var tasks []*BatchTaskRow
	for rows.Next() {
		var task BatchTaskRow
		if err := rows.Scan(
			&task.ID, &task.QueueID, &task.Message, &task.ConversationID,
			&task.Status, &task.StartedAt, &task.CompletedAt, &task.Error, &task.Result,
		); err != nil {
			return nil, fmt.Errorf("扫描批量任务失败: %w", err)
		}
		tasks = append(tasks, &task)
	}

	return tasks, nil
}

// UpdateBatchQueueStatus 更新批量任务队列状态
func (s *BatchTasks) UpdateBatchQueueStatus(queueID, status string) error {
	if err := s.requireDB(); err != nil {
		return err
	}
	var err error
	now := time.Now()

	if status == "running" {
		_, err = s.db.Exec(
			"UPDATE batch_task_queues SET status = ?, started_at = COALESCE(started_at, ?) WHERE id = ?",
			status, now, queueID,
		)
	} else if status == "completed" || status == "cancelled" {
		_, err = s.db.Exec(
			"UPDATE batch_task_queues SET status = ?, completed_at = COALESCE(completed_at, ?) WHERE id = ?",
			status, now, queueID,
		)
	} else {
		_, err = s.db.Exec(
			"UPDATE batch_task_queues SET status = ? WHERE id = ?",
			status, queueID,
		)
	}

	if err != nil {
		return fmt.Errorf("更新批量任务队列状态失败: %w", err)
	}
	return nil
}

// UpdateBatchTaskStatus 更新批量任务状态
func (s *BatchTasks) UpdateBatchTaskStatus(queueID, taskID, status string, conversationID, result, errorMsg string) error {
	if err := s.requireDB(); err != nil {
		return err
	}
	var err error
	now := time.Now()

	// 构建更新语句
	var updates []string
	var args []interface{}

	updates = append(updates, "status = ?")
	args = append(args, status)

	if conversationID != "" {
		updates = append(updates, "conversation_id = ?")
		args = append(args, conversationID)
	}

	if result != "" {
		updates = append(updates, "result = ?")
		args = append(args, result)
	}

	if errorMsg != "" {
		updates = append(updates, "error = ?")
		args = append(args, errorMsg)
	}

	if status == "running" {
		updates = append(updates, "started_at = COALESCE(started_at, ?)")
		args = append(args, now)
	}

	if status == "completed" || status == "failed" || status == "cancelled" {
		updates = append(updates, "completed_at = COALESCE(completed_at, ?)")
		args = append(args, now)
	}

	args = append(args, queueID, taskID)

	// 构建SQL语句
	sql := "UPDATE batch_tasks SET "
	for i, update := range updates {
		if i > 0 {
			sql += ", "
		}
		sql += update
	}
	sql += " WHERE queue_id = ? AND id = ?"

	_, err = s.db.Exec(sql, args...)
	if err != nil {
		return fmt.Errorf("更新批量任务状态失败: %w", err)
	}
	return nil
}

// UpdateBatchQueueCurrentIndex 更新批量任务队列的当前索引
func (s *BatchTasks) UpdateBatchQueueCurrentIndex(queueID string, currentIndex int) error {
	if err := s.requireDB(); err != nil {
		return err
	}
	_, err := s.db.Exec(
		"UPDATE batch_task_queues SET current_index = ? WHERE id = ?",
		currentIndex, queueID,
	)
	if err != nil {
		return fmt.Errorf("更新批量任务队列当前索引失败: %w", err)
	}
	return nil
}

// UpdateBatchQueueMetadata 更新批量任务队列标题、角色、代理模式和并发数
func (s *BatchTasks) UpdateBatchQueueMetadata(queueID, title, role, agentMode string, concurrency int, hitlPolicies ...string) error {
	if err := s.requireDB(); err != nil {
		return err
	}
	if len(hitlPolicies) > 0 {
		_, err := s.db.Exec("UPDATE batch_task_queues SET title = ?, role = ?, agent_mode = ?, concurrency = ?, hitl_policy = ? WHERE id = ?", title, role, agentMode, concurrency, hitlPolicies[0], queueID)
		return err
	}
	_, err := s.db.Exec(
		"UPDATE batch_task_queues SET title = ?, role = ?, agent_mode = ?, concurrency = ? WHERE id = ?",
		title, role, agentMode, concurrency, queueID,
	)
	if err != nil {
		return fmt.Errorf("更新批量任务队列元数据失败: %w", err)
	}
	return nil
}

// UpdateBatchQueueSchedule 更新批量任务队列调度相关信息
func (s *BatchTasks) UpdateBatchQueueSchedule(queueID, scheduleMode, cronExpr string, nextRunAt *time.Time) error {
	if err := s.requireDB(); err != nil {
		return err
	}
	var nextRunAtValue interface{}
	if nextRunAt != nil {
		nextRunAtValue = *nextRunAt
	}
	_, err := s.db.Exec(
		"UPDATE batch_task_queues SET schedule_mode = ?, cron_expr = ?, next_run_at = ? WHERE id = ?",
		scheduleMode, cronExpr, nextRunAtValue, queueID,
	)
	if err != nil {
		return fmt.Errorf("更新批量任务调度配置失败: %w", err)
	}
	return nil
}

// UpdateBatchQueueScheduleEnabled 是否允许 Cron 自动触发（手工「开始执行」不受影响）
func (s *BatchTasks) UpdateBatchQueueScheduleEnabled(queueID string, enabled bool) error {
	if err := s.requireDB(); err != nil {
		return err
	}
	v := 0
	if enabled {
		v = 1
	}
	_, err := s.db.Exec(
		"UPDATE batch_task_queues SET schedule_enabled = ? WHERE id = ?",
		v, queueID,
	)
	if err != nil {
		return fmt.Errorf("更新批量任务调度开关失败: %w", err)
	}
	return nil
}

// RecordBatchQueueScheduledTriggerStart 记录一次由调度触发的开始时间并清空调度层错误
func (s *BatchTasks) RecordBatchQueueScheduledTriggerStart(queueID string, at time.Time) error {
	if err := s.requireDB(); err != nil {
		return err
	}
	_, err := s.db.Exec(
		"UPDATE batch_task_queues SET last_schedule_trigger_at = ?, last_schedule_error = NULL WHERE id = ?",
		at, queueID,
	)
	if err != nil {
		return fmt.Errorf("记录调度触发时间失败: %w", err)
	}
	return nil
}

// SetBatchQueueLastScheduleError 调度启动失败等原因（如状态不允许、重置失败）
func (s *BatchTasks) SetBatchQueueLastScheduleError(queueID, msg string) error {
	if err := s.requireDB(); err != nil {
		return err
	}
	_, err := s.db.Exec(
		"UPDATE batch_task_queues SET last_schedule_error = ? WHERE id = ?",
		msg, queueID,
	)
	if err != nil {
		return fmt.Errorf("写入调度错误信息失败: %w", err)
	}
	return nil
}

// SetBatchQueueLastRunError 最近一轮执行中出现的子任务失败摘要（空串表示清空）
func (s *BatchTasks) SetBatchQueueLastRunError(queueID, msg string) error {
	if err := s.requireDB(); err != nil {
		return err
	}
	var v interface{}
	if strings.TrimSpace(msg) == "" {
		v = nil
	} else {
		v = msg
	}
	_, err := s.db.Exec(
		"UPDATE batch_task_queues SET last_run_error = ? WHERE id = ?",
		v, queueID,
	)
	if err != nil {
		return fmt.Errorf("写入最近运行错误失败: %w", err)
	}
	return nil
}

// ResetBatchQueueForRerun 重置队列和任务状态用于下一轮调度执行
func (s *BatchTasks) ResetBatchQueueForRerun(queueID string) error {
	if err := s.requireDB(); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("开始事务失败: %w", err)
	}
	defer tx.Rollback()

	_, err = tx.Exec(
		"UPDATE batch_task_queues SET status = ?, current_index = 0, started_at = NULL, completed_at = NULL, last_run_error = NULL, last_schedule_error = NULL WHERE id = ?",
		"pending", queueID,
	)
	if err != nil {
		return fmt.Errorf("重置批量任务队列状态失败: %w", err)
	}

	_, err = tx.Exec(
		"UPDATE batch_tasks SET status = ?, conversation_id = NULL, started_at = NULL, completed_at = NULL, error = NULL, result = NULL WHERE queue_id = ?",
		"pending", queueID,
	)
	if err != nil {
		return fmt.Errorf("重置批量任务状态失败: %w", err)
	}

	return tx.Commit()
}

// UpdateBatchTaskMessage 更新批量任务消息
func (s *BatchTasks) UpdateBatchTaskMessage(queueID, taskID, message string) error {
	if err := s.requireDB(); err != nil {
		return err
	}
	_, err := s.db.Exec(
		"UPDATE batch_tasks SET message = ? WHERE queue_id = ? AND id = ?",
		message, queueID, taskID,
	)
	if err != nil {
		return fmt.Errorf("更新批量任务消息失败: %w", err)
	}
	return nil
}

// AddBatchTask 添加任务到批量任务队列
func (s *BatchTasks) AddBatchTask(queueID, taskID, message string) error {
	if err := s.requireDB(); err != nil {
		return err
	}
	_, err := s.db.Exec(
		"INSERT INTO batch_tasks (id, queue_id, message, status) VALUES (?, ?, ?, ?)",
		taskID, queueID, message, "pending",
	)
	if err != nil {
		return fmt.Errorf("添加批量任务失败: %w", err)
	}
	return nil
}

// CancelPendingBatchTasks 批量取消队列中所有 pending 状态的任务（单条 SQL）
func (s *BatchTasks) CancelPendingBatchTasks(queueID string, completedAt time.Time) error {
	if err := s.requireDB(); err != nil {
		return err
	}
	_, err := s.db.Exec(
		"UPDATE batch_tasks SET status = ?, completed_at = ? WHERE queue_id = ? AND status = ?",
		"cancelled", completedAt, queueID, "pending",
	)
	if err != nil {
		return fmt.Errorf("批量取消 pending 任务失败: %w", err)
	}
	return nil
}

// PrepareBatchSingleTaskRun 准备单条执行：可选重置子任务，并更新队列索引与状态
func (s *BatchTasks) PrepareBatchSingleTaskRun(queueID, taskID string, taskIndex int, resetTask, resumeQueue bool) error {
	if err := s.requireDB(); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("开始事务失败: %w", err)
	}
	defer tx.Rollback()

	if resetTask {
		_, err = tx.Exec(
			"UPDATE batch_tasks SET status = ?, conversation_id = NULL, started_at = NULL, completed_at = NULL, error = NULL, result = NULL WHERE queue_id = ? AND id = ?",
			"pending", queueID, taskID,
		)
		if err != nil {
			return fmt.Errorf("重置批量任务状态失败: %w", err)
		}
	}

	if resumeQueue {
		_, err = tx.Exec(
			"UPDATE batch_task_queues SET status = ?, current_index = ?, completed_at = NULL, last_run_error = NULL WHERE id = ?",
			"paused", taskIndex, queueID,
		)
	} else {
		_, err = tx.Exec(
			"UPDATE batch_task_queues SET current_index = ?, last_run_error = NULL WHERE id = ?",
			taskIndex, queueID,
		)
	}
	if err != nil {
		return fmt.Errorf("更新批量任务队列状态失败: %w", err)
	}

	return tx.Commit()
}

// DeleteBatchTask 删除批量任务
func (s *BatchTasks) DeleteBatchTask(queueID, taskID string) error {
	if err := s.requireDB(); err != nil {
		return err
	}
	_, err := s.db.Exec(
		"DELETE FROM batch_tasks WHERE queue_id = ? AND id = ?",
		queueID, taskID,
	)
	if err != nil {
		return fmt.Errorf("删除批量任务失败: %w", err)
	}
	return nil
}

// DeleteBatchQueue 删除批量任务队列
func (s *BatchTasks) DeleteBatchQueue(queueID string) error {
	if err := s.requireDB(); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("开始事务失败: %w", err)
	}
	defer tx.Rollback()

	// 删除任务（外键会自动级联删除）
	_, err = tx.Exec("DELETE FROM batch_tasks WHERE queue_id = ?", queueID)
	if err != nil {
		return fmt.Errorf("删除批量任务失败: %w", err)
	}

	// 删除队列
	_, err = tx.Exec("DELETE FROM batch_task_queues WHERE id = ?", queueID)
	if err != nil {
		return fmt.Errorf("删除批量任务队列失败: %w", err)
	}

	return tx.Commit()
}
