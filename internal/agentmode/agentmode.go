// Package agentmode 是「对话模式」这套身份的单一来源。
//
// 在此之前同一份知识散在至少五处：config 里两份 Normalize、store 一份、机器人一份、
// workflow 内联一份，别名集和默认值还互不一致（例如 "pe" 走会话路径会变成 eino_single、
// 走机器人路径却是 plan_execute）。这里的表是全仓唯一的声明：
//
//	谁存在（id）、怎么被人叫（别名 union，含中文与历史拼写）、叫什么（label / i18n key）、
//	谁来执行（runner）、什么时候可用（requires）。
//
// 本包零 internal 依赖：store / config / workflow / handler 都要引用它，所以它自己不引用
// 任何一层。读能力表（哪些模式被单元激活）通过 Unit/Build 的窄形状由调用方适配，
// 不 import internal/plugin。
//
// 「能力化」的边界：模式声明文件只能*激活*内核已知的模式，改不了执行绑定——runner 与
// 编排名是内核知识，一个包能做的只是让 deep/plan_execute/supervisor 出现在目录里
// （eino_single 是内置底线，不允许被单元覆盖）。新模式进目录需要先在内核表里登记。
package agentmode

import "strings"

// Runner 是内核认识的执行器。模式只是它的一个入口名字。
type Runner string

const (
	// RunnerEinoSingle 走单代理 ADK runner（multiagent.RunEinoSingleChatModelAgent）。
	RunnerEinoSingle Runner = "eino_single"
	// RunnerMultiAgent 走多代理编排 runner（multiagent.RunDeepAgent），由 Orchestration 选择编排。
	RunnerMultiAgent Runner = "multi_agent"
)

// Requires 是模式可用的前提条件。
type Requires string

const (
	RequiresNone       Requires = ""
	RequiresMultiAgent Requires = "multi_agent"
)

// Mode 是内核已知的一条对话模式。
type Mode struct {
	ID            string
	Label         string // 无 i18n 场景的文案（机器人文本、日志）
	LabelKey      string // 前端 i18n key（选择器名称行）
	HintKey       string // 前端 i18n key（选择器描述行）
	Aliases       []string
	Runner        Runner
	Orchestration string // 仅 RunnerMultiAgent：编排名
	Requires      Requires
	Order         int
	// Builtin 的模式恒在目录里、不可被单元覆盖或停用（eino_single 是产品底线）。
	Builtin bool
}

// DefaultID 是「没有选择」时代替一切的选择。会话、机器人、批量任务都用它兜底。
const DefaultID = "eino_single"

// DefaultOrchestration 是多代理请求缺省时就近的编排（与 DefaultID 是同一类兜底，
// 但多代理参数空间里没有 eino_single，所以是 deep）。
const DefaultOrchestration = "deep"

var known = []Mode{
	{
		ID:    "eino_single",
		Label: "Eino 单代理", LabelKey: "chat.agentModeEinoSingle", HintKey: "chat.agentModeEinoSingleHint",
		Aliases: []string{"eino_single", "eino-single", "single", "chat", "单代理", "eino单代理", "eino 单代理"},
		Runner:  RunnerEinoSingle,
		Order:   10,
		Builtin: true,
	},
	{
		ID:    "deep",
		Label: "Deep", LabelKey: "chat.agentModeDeep", HintKey: "chat.agentModeDeepHint",
		// "multi" 是前端早期存过的值（webshell.js 的 normalize 至今把它并到 deep）。
		Aliases: []string{"deep", "multi"},
		Runner:  RunnerMultiAgent, Orchestration: "deep",
		Requires: RequiresMultiAgent,
		Order:    20,
	},
	{
		ID:    "plan_execute",
		Label: "Plan-Execute", LabelKey: "chat.agentModePlanExecuteLabel", HintKey: "chat.agentModePlanExecuteHint",
		Aliases: []string{"plan_execute", "plan-execute", "planexecute", "pe"},
		Runner:  RunnerMultiAgent, Orchestration: "plan_execute",
		Requires: RequiresMultiAgent,
		Order:    30,
	},
	{
		ID:    "supervisor",
		Label: "Supervisor", LabelKey: "chat.agentModeSupervisorLabel", HintKey: "chat.agentModeSupervisorHint",
		Aliases: []string{"supervisor", "super", "sv"},
		Runner:  RunnerMultiAgent, Orchestration: "supervisor",
		Requires: RequiresMultiAgent,
		Order:    40,
	},
}

var (
	byID    = map[string]Mode{}
	byAlias = map[string]string{}
)

func init() {
	for _, m := range known {
		byID[m.ID] = m
		byAlias[m.ID] = m.ID
		for _, a := range m.Aliases {
			byAlias[canonicalKey(a)] = m.ID
		}
	}
}

// canonicalKey 是别名查找前的归一：去空白、小写、连字符视为下划线。
// 机器人输入（含中文别名）与前端的拼写变体都走同一把尺子。
func canonicalKey(input string) string {
	s := strings.ToLower(strings.TrimSpace(input))
	return strings.ReplaceAll(s, "-", "_")
}

// Canonical 把任意用户输入解析成规范模式 id。空输入与未知输入都返回 ok=false——
// 各入口的默认值本来就不同（会话默认 eino_single、多代理编排参数默认 deep），
// 所以这里不做兜底，由调用方按自己的语义定默认。
func Canonical(input string) (string, bool) {
	key := canonicalKey(input)
	if key == "" {
		return "", false
	}
	id, ok := byAlias[key]
	return id, ok
}

// Known 按规范 id 查内核已知模式。输入会先归一（大小写/连字符），但不接受别名。
func Known(id string) (Mode, bool) {
	m, ok := byID[canonicalKey(id)]
	return m, ok
}

// All 按展示顺序返回全部内核已知模式。
func All() []Mode {
	out := make([]Mode, len(known))
	copy(out, known)
	return out
}

// AllIDs 按展示顺序返回全部内核已知模式 id——完整清单出现的地方（MCP 工具参数枚举、
// 文档生成）用它，而不是再抄一份字面量表。
func AllIDs() []string {
	out := make([]string, 0, len(known))
	for _, m := range known {
		out = append(out, m.ID)
	}
	return out
}

// Activatable 判断一个 id 是否允许被模式单元激活：内核认识且不是内置底线。
func Activatable(id string) bool {
	m, ok := byID[canonicalKey(id)]
	return ok && !m.Builtin
}

// RunnerFor 回答「这个规范 id 由哪个 runner 执行」。
func RunnerFor(id string) (Runner, string, bool) {
	m, ok := byID[canonicalKey(id)]
	if !ok {
		return "", "", false
	}
	return m.Runner, m.Orchestration, true
}

// ResolveWithDefault 是 Canonical 的宽容版本：解析失败时给出给定的兜底。
func ResolveWithDefault(input string, fallback string) string {
	if id, ok := Canonical(input); ok {
		return id
	}
	return fallback
}

// ResolveOrchestration 把输入限制到多代理编排空间：命中多代理模式返回其 id，
// 其余（含 eino_single、空、垃圾输入）一律回落 DefaultOrchestration。
// 这是 /api/multi-agent 请求参数与 multi_agent.orchestration 配置的既有语义。
func ResolveOrchestration(input string) string {
	if id, ok := Canonical(input); ok {
		if m, found := byID[id]; found && m.Runner == RunnerMultiAgent {
			return id
		}
	}
	return DefaultOrchestration
}
