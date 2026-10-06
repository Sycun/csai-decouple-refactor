package audit

import (
	"strings"

	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/mcp"
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

// ResourceExistenceSource is the read surface this check needs: given the resource an audit row
// points at, does that row still exist? Declared here because internal/audit is the consumer, and
// listing seven lookups is cheaper than handing the audit log reader the whole connection wrapper.
//
// The handlers' own store interfaces must be a superset of this one, since they pass their storage
// in - internal/database/stores.go keeps ResourceExistence aligned with it.
type ResourceExistenceSource interface {
	ConversationExists(id string) (bool, error)
	GetBatchQueue(queueID string) (*database.BatchTaskQueueRow, error)
	GetC2Listener(id string) (*database.C2Listener, error)
	GetC2Session(id string) (*database.C2Session, error)
	GetC2Task(id string) (*database.C2Task, error)
	GetWebshellConnection(id string) (*database.WebShellConnection, error)
	GetToolExecution(id string) (*mcp.ToolExecution, error)
}

// FindingLookup is the findings answer split out, because that read is store.Vulnerabilities.Get now
// rather than a method on the connection wrapper. A caller without one (no database wired) passes nil
// and the audit row keeps answering "availability unknown" instead of "this finding is gone".
type FindingLookup interface {
	Get(id string) (*store.Vulnerability, error)
}

// ApplyResourceAvailability sets log.ResourceAvailable when the linked resource can be checked.
//
// db is an interface, so callers must pass one built by database.Narrow: a nil *database.DB stored
// in an interface is not nil, and the guard below would then fall through into method calls on a nil
// receiver instead of reporting "availability unknown".
func ApplyResourceAvailability(db ResourceExistenceSource, findings FindingLookup, log *store.AuditLog) {
	if log == nil || strings.TrimSpace(log.ResourceID) == "" {
		return
	}
	if auditActionsResourceRemoved[log.Action] {
		f := false
		log.ResourceAvailable = &f
		return
	}
	if db == nil {
		return
	}
	available, known := resourceStillExists(db, findings, log.ResourceType, log.ResourceID)
	if known {
		log.ResourceAvailable = &available
	}
}

func resourceStillExists(db ResourceExistenceSource, findings FindingLookup, resourceType, resourceID string) (bool, bool) {
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
		ok, err := db.ConversationExists(resourceID)
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
		_, err := db.GetBatchQueue(resourceID)
		return err == nil, true
	case "c2_listener":
		_, err := db.GetC2Listener(resourceID)
		return err == nil, true
	case "c2_session":
		_, err := db.GetC2Session(resourceID)
		return err == nil, true
	case "c2_task":
		_, err := db.GetC2Task(resourceID)
		return err == nil, true
	case "webshell_connection":
		c, err := db.GetWebshellConnection(resourceID)
		return err == nil && c != nil, true
	case "tool_execution":
		_, err := db.GetToolExecution(resourceID)
		return err == nil, true
	default:
		return false, false
	}
}
