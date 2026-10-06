package app

import (
	"fmt"
	"strings"

	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/store"
)

// Resource-to-project resolution used by the capability policy adapter. These
// are data lookups only: every authorization decision goes through
// internal/capability, so no second decision path can drift.
func mcpResourceProjectID(db *database.DB, c2store *store.C2, resourceType, resourceID string) (string, bool, error) {
	webshells := database.NewWebshell(db)
	switch resourceType {
	case "webshell":
		conn, err := webshells.Get(resourceID)
		if err != nil {
			return "", true, err
		}
		if conn == nil {
			return "", true, fmt.Errorf("webshell not found")
		}
		return strings.TrimSpace(conn.ProjectID), true, nil
	case "c2_listener":
		listener, err := c2store.GetC2Listener(resourceID)
		if err != nil {
			return "", true, err
		}
		if listener == nil {
			return "", true, fmt.Errorf("listener not found")
		}
		return strings.TrimSpace(listener.ProjectID), true, nil
	case "c2_session":
		session, err := c2store.GetC2Session(resourceID)
		if err != nil {
			return "", true, err
		}
		if session == nil {
			return "", true, fmt.Errorf("session not found")
		}
		return mcpResourceProjectID(db, c2store, "c2_listener", session.ListenerID)
	case "c2_task":
		task, err := c2store.GetC2Task(resourceID)
		if err != nil {
			return "", true, err
		}
		if task == nil {
			return "", true, fmt.Errorf("task not found")
		}
		return mcpResourceProjectIDFromC2Session(db, c2store, task.SessionID)
	default:
		return "", false, nil
	}
}

func mcpResourceProjectIDFromC2Session(db *database.DB, c2store *store.C2, sessionID string) (string, bool, error) {
	session, err := c2store.GetC2Session(sessionID)
	if err != nil {
		return "", true, err
	}
	if session == nil {
		return "", true, fmt.Errorf("session not found")
	}
	return mcpResourceProjectID(db, c2store, "c2_listener", session.ListenerID)
}

func mcpAuthorizationStrings(args map[string]interface{}, key string) []string {
	values := []string{}
	switch raw := args[key].(type) {
	case []string:
		for _, value := range raw {
			if value = strings.TrimSpace(value); value != "" {
				values = append(values, value)
			}
		}
	case []interface{}:
		for _, item := range raw {
			if value, ok := item.(string); ok {
				if value = strings.TrimSpace(value); value != "" {
					values = append(values, value)
				}
			}
		}
	}
	return values
}

func mcpAuthorizationString(args map[string]interface{}, key string) string {
	value, _ := args[key].(string)
	return strings.TrimSpace(value)
}
