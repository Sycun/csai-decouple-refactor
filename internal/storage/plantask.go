package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
)

// PlanTask mirrors the public fields Eino's plantask backend persists per task file.
//
// It lives here because reading that board is a walk over one conversation's directory, not a query:
// the data layer only carries it because that is where the conversation's directory root is configured.
// Keeping the transport model out of the Eino private type is the same reason it was written in the
// first place.
type PlanTask struct {
	ID          string   `json:"id"`
	Subject     string   `json:"subject"`
	Description string   `json:"description,omitempty"`
	Status      string   `json:"status"`
	Blocks      []string `json:"blocks,omitempty"`
	BlockedBy   []string `json:"blockedBy,omitempty"`
	ActiveForm  string   `json:"activeForm,omitempty"`
	Owner       string   `json:"owner,omitempty"`
}

// ReadPlanTasks lists the live task board in `dir`, optionally limited to files touched at or after
// `since` (Eino keeps older task files for model continuity, while a new run must not surface them
// before its own TaskCreate). A missing directory is the normal state for short or legacy
// conversations, so it answers an empty list rather than an error.
//
// Unreadable and undecodable files are skipped with a Debug line: TaskUpdate writes these files
// concurrently with this read, so a partial read is transient and the next poll recovers. logger may
// be nil, which keeps the same result and loses only the diagnostic.
func ReadPlanTasks(dir string, since time.Time, logger *zap.Logger) ([]PlanTask, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return []PlanTask{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read conversation plan tasks: %w", err)
	}

	type numberedTask struct {
		number int
		task   PlanTask
	}
	numbered := make([]numberedTask, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		idText := strings.TrimSuffix(entry.Name(), ".json")
		number, parseErr := strconv.Atoi(idText)
		if parseErr != nil || number < 1 {
			continue
		}
		if !since.IsZero() {
			info, infoErr := entry.Info()
			if infoErr != nil {
				continue
			}
			if info.ModTime().Before(since) {
				continue
			}
		}
		content, readErr := os.ReadFile(filepath.Join(dir, entry.Name()))
		if readErr != nil {
			if logger != nil {
				logger.Debug("读取 Eino 任务文件失败",
					zap.String("dir", dir),
					zap.String("file", entry.Name()),
					zap.Error(readErr))
			}
			continue
		}
		var task PlanTask
		if decodeErr := json.Unmarshal(content, &task); decodeErr != nil {
			if logger != nil {
				logger.Debug("解析 Eino 任务文件失败",
					zap.String("dir", dir),
					zap.String("file", entry.Name()),
					zap.Error(decodeErr))
			}
			continue
		}
		if strings.TrimSpace(task.ID) == "" {
			task.ID = idText
		}
		if strings.EqualFold(strings.TrimSpace(task.Status), "deleted") {
			continue
		}
		numbered = append(numbered, numberedTask{number: number, task: task})
	}

	// File name order is the board order the agent wrote; a stable sort keeps same-number ties
	// (impossible in practice, but cheap to keep deterministic) reproducible.
	sort.SliceStable(numbered, func(i, j int) bool {
		return numbered[i].number < numbered[j].number
	})
	tasks := make([]PlanTask, 0, len(numbered))
	for _, item := range numbered {
		tasks = append(tasks, item.task)
	}
	return tasks, nil
}
