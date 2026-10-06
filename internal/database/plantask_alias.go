package database

import "cyberstrike-ai/internal/storage"

// ConversationPlanTask 的定义在 internal/storage（任务看板本来就是读盘）；这里留别名让调用点
// 读法不变。原来的 ListConversationPlanTasks（无 since 版本，已被 Since 取代）按死方法规矩删除。
type ConversationPlanTask = storage.PlanTask
