package audit

import (
	"strings"

	"cyberstrike-ai/internal/store"
)

var auditActionsResourceRemoved = map[string]bool{
	"delete":                 true,
	"item_delete":            true,
	"connection_delete":      true,
	"listener_delete":        true,
	"session_delete":         true,
	"task_delete":            true,
	"execution_delete":       true,
	"execution_delete_batch": true,
	"delete_queue":           true,
	"delete_batch_task":      true,
	"markdown_delete":        true,
}

// ResourceExistenceSource 已随最后一条问句（ConversationExists）交回会话 store 而消失：每一种
// 资源的存在性现在都由它自己的 store 回答，这个检查只做组合。

// BatchQueueLookup is the batch queue answer split out for the same reason as the two below: that
// read is store.BatchTasks.GetBatchQueue now, not a method on the connection wrapper.
type BatchQueueLookup interface {
	GetBatchQueue(queueID string) (*store.BatchTaskQueueRow, error)
}

// FindingLookup is the findings answer split out, because that read is store.Vulnerabilities.Get now
// rather than a method on the connection wrapper. A caller without one (no database wired) passes nil
// and the audit row keeps answering "availability unknown" instead of "this finding is gone".
type FindingLookup interface {
	Get(id string) (*store.Vulnerability, error)
}

// WebshellLookup is the webshell answer split out the same way, because that read is
// store.Webshell.Get now rather than a method on the connection wrapper.
type WebshellLookup interface {
	Get(id string) (*store.WebShellConnection, error)
}

// ApplyResourceAvailability sets log.ResourceAvailable when the linked resource can be checked.
//
// db is an interface, so callers must pass one built by database.Narrow: a nil *database.DB stored
// in an interface is not nil, and the guard below would then fall through into method calls on a nil
// receiver instead of reporting "availability unknown". c2 and executions are concrete pointers,
// so a plain nil check is exact there and those branches answer "unknown" without a database.
func ApplyResourceAvailability(conversations *store.Conversations, c2 *store.C2, executions *store.Monitor, findings FindingLookup, webshells WebshellLookup, batches BatchQueueLookup, log *store.AuditLog) {
	if log == nil || strings.TrimSpace(log.ResourceID) == "" {
		return
	}
	if auditActionsResourceRemoved[log.Action] {
		f := false
		log.ResourceAvailable = &f
		return
	}
	if conversations == nil && c2 == nil && executions == nil && findings == nil && webshells == nil && batches == nil {
		return
	}
	available, known := resourceStillExists(conversations, c2, executions, findings, webshells, batches, log.ResourceType, log.ResourceID)
	if known {
		log.ResourceAvailable = &available
	}
}

func resourceStillExists(conversations *store.Conversations, c2 *store.C2, executions *store.Monitor, findings FindingLookup, webshells WebshellLookup, batches BatchQueueLookup, resourceType, resourceID string) (bool, bool) {
	resourceID = strings.TrimSpace(resourceID)
	if resourceID == "" {
		return false, false
	}
	t := strings.TrimSpace(resourceType)
	if t == "" {
		if len(resourceID) > 8 && !strings.HasPrefix(resourceID, "c2_") {
			t = "conversation"
		} else {
			return false, false
		}
	}
	switch t {
	case "conversation":
		if conversations == nil {
			return false, false
		}
		ok, err := conversations.ConversationExists(resourceID)
		return ok, err == nil
	case "vulnerability":
		if findings == nil {
			return false, false
		}
		_, err := findings.Get(resourceID)
		if err != nil {
			return false, strings.Contains(err.Error(), "不存在")
		}
		return true, true
	case "batch_queue":
		if batches == nil {
			return false, false
		}
		_, err := batches.GetBatchQueue(resourceID)
		return err == nil, true
	case "c2_listener":
		if c2 == nil {
			return false, false
		}
		_, err := c2.GetC2Listener(resourceID)
		return err == nil, true
	case "c2_session":
		if c2 == nil {
			return false, false
		}
		_, err := c2.GetC2Session(resourceID)
		return err == nil, true
	case "c2_task":
		if c2 == nil {
			return false, false
		}
		_, err := c2.GetC2Task(resourceID)
		return err == nil, true
	case "webshell_connection":
		if webshells == nil {
			return false, false
		}
		c, err := webshells.Get(resourceID)
		return err == nil && c != nil, true
	case "tool_execution":
		if executions == nil {
			return false, false
		}
		_, err := executions.GetToolExecution(resourceID)
		return err == nil, true
	default:
		return false, false
	}
}
