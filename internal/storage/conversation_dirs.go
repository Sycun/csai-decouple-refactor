package storage

import (
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"
)

// ConversationPathSegment turns a conversation or project id into one directory name.
//
// It is the single sanitizer on the live path: the board read, the artifact lookup and every
// start-up cleanup have to agree, or a conversation can end up with a directory that is listed but
// never deleted. It used to live in the data layer, where the cleanup it now guards did not belong.
func ConversationPathSegment(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "default"
	}
	s = strings.ReplaceAll(s, string(filepath.Separator), "-")
	s = strings.ReplaceAll(s, "/", "-")
	s = strings.ReplaceAll(s, "\\", "-")
	s = strings.ReplaceAll(s, "..", "__")
	if len(s) > 180 {
		s = s[:180]
	}
	return s
}

// ConversationDirs is the set of per-conversation directories an agent run leaves on disk.
// The roots are injected by the wiring layer from the same values storage.Paths is built from, so
// the cleaner and the writer can never drift apart; the two Eino scratch roots keep the defaults the
// data layer used to apply here ("tmp/reduction" / "tmp/workspace").
type ConversationDirs struct {
	Artifacts   string // conversation_artifacts/<id>
	Plantask    string // skills_dir/<plantask_rel>/<id>
	Checkpoint  string // checkpoint_dir/<id>
	Reduction   string // <root>/conversations/<id>, or projects/<id> for a project-bound run
	Workspace   string // <root>/conversations/<id>, or projects/<id>
	ChatUploads string // <root>/<date>/<id> - one layer deeper than every other entry here

	logger *zap.Logger
}

// NewConversationDirs resolves the roots. Passing an empty Reduction or Workspace keeps the
// historical default rather than deleting nothing.
func NewConversationDirs(spec ConversationDirs, logger *zap.Logger) ConversationDirs {
	d := spec
	d.logger = logger
	if strings.TrimSpace(d.Reduction) == "" {
		d.Reduction = filepath.Join("tmp", "reduction")
	}
	if strings.TrimSpace(d.Workspace) == "" {
		d.Workspace = filepath.Join("tmp", "workspace")
	}
	return d
}

// removeScoped deletes one `<base>/<id>` directory. An empty base means that root was never
// configured, which is the normal state for an optional feature, so it is not a failure.
func (d ConversationDirs) removeScoped(base, id, label string) {
	base = strings.TrimSpace(base)
	if base == "" {
		return
	}
	dir := filepath.Join(base, ConversationPathSegment(id))
	if rmErr := os.RemoveAll(dir); rmErr != nil {
		if d.logger != nil {
			d.logger.Warn("删除会话目录失败",
				zap.String("conversationId", id),
				zap.String("kind", label),
				zap.String("dir", dir),
				zap.Error(rmErr))
		}
	}
}

// RemoveConversation deletes every per-conversation directory an ended conversation still owns.
// Uploads are always removed - they belong to one conversation even when it is bound to a project -
// while the two project-shared scratch roots are only cleared for a standalone conversation, since
// a project-bound run shares projects/<id>/ with its siblings.
func (d ConversationDirs) RemoveConversation(conversationID, projectID string) {
	d.removeScoped(d.Artifacts, conversationID, "conversation_artifacts")
	d.removeScoped(d.Plantask, conversationID, "plantask")
	d.removeScoped(d.Checkpoint, conversationID, "eino_checkpoint")
	d.removeChatUploads(conversationID)
	if strings.TrimSpace(projectID) == "" {
		d.removeScoped(filepath.Join(d.Reduction, "conversations"), conversationID, "reduction")
		d.removeScoped(filepath.Join(d.Workspace, "conversations"), conversationID, "workspace")
	}
}

// RemoveProject deletes the project-scoped scratch directories a removed project owned.
func (d ConversationDirs) RemoveProject(projectID string) {
	d.removeScoped(filepath.Join(d.Reduction, "projects"), projectID, "reduction")
	d.removeScoped(filepath.Join(d.Workspace, "projects"), projectID, "workspace")
}

// removeChatUploads walks the date layer under the uploads root, which is why it cannot reuse
// removeScoped: uploads live at <root>/<date>/<conversationID>.
func (d ConversationDirs) removeChatUploads(conversationID string) {
	base := strings.TrimSpace(d.ChatUploads)
	if base == "" || strings.TrimSpace(conversationID) == "" {
		return
	}
	seg := ConversationPathSegment(conversationID)
	dates, err := os.ReadDir(base)
	if err != nil {
		return
	}
	for _, dateDir := range dates {
		if !dateDir.IsDir() {
			continue
		}
		dir := filepath.Join(base, dateDir.Name(), seg)
		if rmErr := os.RemoveAll(dir); rmErr != nil {
			if d.logger != nil {
				d.logger.Warn("删除会话上传目录失败",
					zap.String("conversationId", conversationID),
					zap.String("kind", "chat_uploads"),
					zap.String("dir", dir),
					zap.Error(rmErr))
			}
		}
	}
}
