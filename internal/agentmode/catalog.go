package agentmode

import "fmt"

// Unit 是读侧看到的一个已登记的 mode 单元。由调用方从能力表映射成这个窄形状，
// 本包因此不依赖 internal/plugin——表、包、安装记录的关系留在能力平台那一层。
type Unit struct {
	Name    string
	Path    string
	Bundle  string
	Enabled bool
}

// Entry 是目录里的一条：某个模式此刻的可见性与可用性。
type Entry struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	LabelKey  string `json:"labelKey,omitempty"`
	HintKey   string `json:"hintKey,omitempty"`
	Runner    Runner `json:"runner,omitempty"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	Builtin   bool   `json:"builtin,omitempty"`
	Bundle    string `json:"bundle,omitempty"`
}

// ReasonEngineDisabled：模式已被包激活，但多代理引擎（multi_agent.enabled）没启用。
const ReasonEngineDisabled = "engine_disabled"

// Build 合并「内核内置模式」与「被单元激活的模式」，得到此刻的目录。
//
// 三条语义：
//   - 内置（eino_single）恒在且可用——它是产品底线，与装了什么包无关；
//   - 非内置模式只在被已启用的单元激活时出现（"不点不存在"）。停用与卸载同义，
//     不做「存在但灰掉」的中间态；
//   - 未知 id、或试图覆盖内置的单元不进目录。写入路径（安装校验、启动核对）负责
//     拒绝并指名；这里静默跳过是运行时防线，不是许可。
func Build(units []Unit, engineEnabled bool) []Entry {
	active := map[string]string{}
	for _, u := range units {
		if !u.Enabled {
			continue
		}
		m, ok := byID[canonicalKey(u.Name)]
		if !ok || m.Builtin {
			continue
		}
		active[m.ID] = u.Bundle
	}
	out := make([]Entry, 0, len(known))
	for _, m := range known {
		e := Entry{ID: m.ID, Label: m.Label, LabelKey: m.LabelKey, HintKey: m.HintKey, Runner: m.Runner, Builtin: m.Builtin}
		if m.Builtin {
			e.Available = true
			out = append(out, e)
			continue
		}
		bundle, ok := active[m.ID]
		if !ok {
			continue
		}
		e.Bundle = bundle
		if m.Requires == RequiresMultiAgent {
			e.Available = engineEnabled
			if !engineEnabled {
				e.Reason = ReasonEngineDisabled
			}
		} else {
			e.Available = true
		}
		out = append(out, e)
	}
	return out
}

// Lookup 在目录里找一条已激活的模式（含不可用的）。
func Lookup(units []Unit, engineEnabled bool, id string) (Entry, bool) {
	for _, e := range Build(units, engineEnabled) {
		if e.ID == id {
			return e, true
		}
	}
	return Entry{}, false
}

// Check 回答「这个模式此刻能不能执行」。不可用时 error 指名原因：
// 内核不认识 / 未被激活（包未安装或单元停用）/ 引擎未启用。它是执行入口的
// fail-closed 判据——卸载包之后，带着 deep 的存量会话在这里被挡住并被告知为什么。
func Check(units []Unit, engineEnabled bool, id string) (Entry, error) {
	m, ok := byID[canonicalKey(id)]
	if !ok {
		return Entry{}, fmt.Errorf("内核不认识的对话模式 %q", id)
	}
	if m.Builtin {
		return Entry{ID: m.ID, Label: m.Label, LabelKey: m.LabelKey, HintKey: m.HintKey, Runner: m.Runner, Available: true, Builtin: true}, nil
	}
	e, ok := Lookup(units, engineEnabled, m.ID)
	if !ok {
		return Entry{}, fmt.Errorf("对话模式 %s 未安装：多代理编排包未安装，或该模式单元已被停用", m.Label)
	}
	if !e.Available {
		return e, fmt.Errorf("对话模式 %s 暂不可用：需要先在系统设置中启用 Eino 多代理", m.Label)
	}
	return e, nil
}
