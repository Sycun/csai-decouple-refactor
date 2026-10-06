package database

import "cyberstrike-ai/internal/store"

// 项目域的语句（8 个方法 + 统计/仪表盘/活跃度）已交回 store.Projects；行类型在 store（决策 42
// 的词汇别名同款），database 侧留别名让还没轮到搬迁的调用点读法不变——声明只有一份。
type (
	Project                 = store.Project
	ProjectStats            = store.ProjectStats
	ProjectDashboardFact    = store.ProjectDashboardFact
	ProjectDashboardTotals  = store.ProjectDashboardTotals
	ProjectDashboardSummary = store.ProjectDashboardSummary
)
