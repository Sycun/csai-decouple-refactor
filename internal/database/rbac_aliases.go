package database

import "cyberstrike-ai/internal/store"

// RBAC 的词汇与语句已交回 store.RBAC。这里留别名，让还没轮到搬迁的调用点（handler / security /
// app 里的 permission 目录与窄接口签名）读法不变——声明只有一份（store），别名不是第二实现。
const (
	RBACSystemRoleAdmin    = store.RBACSystemRoleAdmin
	RBACSystemRoleOperator = store.RBACSystemRoleOperator
	RBACSystemRoleAuditor  = store.RBACSystemRoleAuditor
	RBACSystemRoleViewer   = store.RBACSystemRoleViewer

	RBACScopeAll      = store.ScopeAll
	RBACScopeAssigned = store.ScopeAssigned
	RBACScopeOwn      = store.ScopeOwn

	RBACMaxBatchResourceAssignments = store.RBACMaxBatchResourceAssignments
)

type (
	RBACUser               = store.RBACUser
	RBACRole               = store.RBACRole
	RBACResourceAssignment = store.RBACResourceAssignment
	RBACResourceOption     = store.RBACResourceOption
	RBACAccess             = store.RBACAccess
)
