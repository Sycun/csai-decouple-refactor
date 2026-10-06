package database

import "cyberstrike-ai/internal/store"

// 会话域的语句（42 个方法）与行类型已交回 store.Conversations / store.Session；这里留别名，
// 让还没轮到搬迁的调用点（handler / agent / mcp / finalizer 的类型签名）读法不变——
// 声明只有一份（store），别名不是第二实现。
type (
	Conversation                = store.Conversation
	Message                     = store.Message
	WebShellConversationItem    = store.WebShellConversationItem
	ProcessDetail               = store.ProcessDetail
	AssistantCognitionTexts     = store.AssistantCognitionTexts
	ProcessDetailsSummary       = store.ProcessDetailsSummary
	ProcessDetailsToolExecution = store.ProcessDetailsToolExecution
	ConversationCreateMeta      = store.ConversationCreateMeta
	ConversationCreateHook      = store.ConversationCreateHook
)
