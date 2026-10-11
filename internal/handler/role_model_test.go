package handler

import (
	"testing"

	"cyberstrike-ai/internal/config"

	"go.uber.org/zap"
)

// roleModelTestConfig is a deployment with three channels and three roles that exercise the
// three ways a binding can be wrong or absent: bound, unbound, and bound to something gone.
func roleModelTestConfig() *config.Config {
	cfg := &config.Config{}
	cfg.AI.DefaultChannel = "default"
	cfg.AI.Channels = map[string]config.AIChannelConfig{
		"default": {Name: "Default", Model: "model-default", APIKey: "k", BaseURL: "http://default"},
		"fast":    {Name: "Fast", Model: "model-fast", APIKey: "k", BaseURL: "http://fast"},
		"deep":    {Name: "Deep", Model: "model-deep", APIKey: "k", BaseURL: "http://deep"},
	}
	cfg.Roles = map[string]config.RoleConfig{
		"分析师": {Name: "分析师", Enabled: true, AIChannel: "deep"},
		"默认":  {Name: "默认", Enabled: true},
		// Saved while the channel existed, deleted afterwards. Must not take a run down.
		"陈旧": {Name: "陈旧", Enabled: true, AIChannel: "gone"},
		// A hand-edited file with the display name rather than the key.
		"手写": {Name: "手写", Enabled: true, AIChannel: " Fast "},
	}
	return cfg
}

func TestEffectiveAIChannelIDPrecedence(t *testing.T) {
	cfg := roleModelTestConfig()
	logger := zap.NewNop()

	cases := []struct {
		name     string
		explicit string
		role     string
		want     string
		why      string
	}{
		{"explicit beats role", "fast", "分析师", "fast", "the session picker is a deliberate override"},
		{"role binds", "", "分析师", "deep", "one role, one model"},
		{"role without binding", "", "默认", "", "empty means follow ai.default_channel"},
		{"stale binding falls back", "", "陈旧", "", "a deleted channel must not fail the run"},
		{"no role", "", "", "", "nothing to bind"},
		{"unknown role", "", "不存在", "", "an unknown role is not an error here"},
		{"hand-written id is normalised", "", "手写", "fast", "resolve what the operator meant, not the literal bytes"},
		{"whitespace-only explicit", "   ", "分析师", "deep", "a blank picker is no choice at all"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := effectiveAIChannelID(cfg, tc.explicit, tc.role, logger); got != tc.want {
				t.Fatalf("effectiveAIChannelID(%q, %q) = %q, want %q (%s)", tc.explicit, tc.role, got, tc.want, tc.why)
			}
		})
	}
}

func TestConfigForAIChannelAppliesRoleModel(t *testing.T) {
	cfg := roleModelTestConfig()
	h := &AgentHandler{config: cfg, logger: zap.NewNop()}

	runCfg, id, err := h.configForAIChannel("", "分析师")
	if err != nil {
		t.Fatalf("configForAIChannel: %v", err)
	}
	if id != "deep" {
		t.Fatalf("resolved channel = %q, want %q", id, "deep")
	}
	if runCfg.OpenAI.Model != "model-deep" {
		t.Fatalf("run model = %q, want %q: the role's channel must reach the model the run uses", runCfg.OpenAI.Model, "model-deep")
	}
	if runCfg == cfg {
		t.Fatal("runCfg must be a copy; mutating the live snapshot would leak one role's model into every other run")
	}

	// An explicit session channel still wins over the role's binding.
	runCfg, id, err = h.configForAIChannel("fast", "分析师")
	if err != nil {
		t.Fatalf("configForAIChannel(explicit): %v", err)
	}
	if id != "fast" || runCfg.OpenAI.Model != "model-fast" {
		t.Fatalf("explicit selection gave (%q, %q), want (fast, model-fast)", id, runCfg.OpenAI.Model)
	}

	// A role with no binding keeps the behaviour every caller had before: the default channel.
	runCfg, id, err = h.configForAIChannel("", "默认")
	if err != nil {
		t.Fatalf("configForAIChannel(unbound role): %v", err)
	}
	if id != "default" || runCfg.OpenAI.Model != "model-default" {
		t.Fatalf("unbound role gave (%q, %q), want (default, model-default)", id, runCfg.OpenAI.Model)
	}

	// And a stale binding degrades to the default instead of erroring out.
	if _, id, err = h.configForAIChannel("", "陈旧"); err != nil || id != "default" {
		t.Fatalf("stale binding gave (%q, %v), want (default, nil)", id, err)
	}
}

func TestNormalizeRoleAIChannelKeepsEmptyEmpty(t *testing.T) {
	// config.NormalizeAIChannelID("") == "default". Passing an unbound role's empty field
	// through it would pin every "follow the default" role to a channel named default.
	if got := normalizeRoleAIChannel(""); got != "" {
		t.Fatalf(`normalizeRoleAIChannel("") = %q, want "": empty means "follow the default"`, got)
	}
	if got := normalizeRoleAIChannel("   "); got != "" {
		t.Fatalf("normalizeRoleAIChannel(whitespace) = %q, want empty", got)
	}
	if got := normalizeRoleAIChannel("Qwen Max"); got != "qwen-max" {
		t.Fatalf("normalizeRoleAIChannel(%q) = %q, want %q", "Qwen Max", got, "qwen-max")
	}
}

func TestValidateRoleAIChannelBinding(t *testing.T) {
	cfg := roleModelTestConfig()
	h := NewRoleHandler(cfg, "", zap.NewNop(), nil, nil)

	if err := h.validateRole(config.RoleConfig{Name: "新角色", Enabled: true, AIChannel: "fast"}); err != nil {
		t.Fatalf("a channel that exists must be accepted: %v", err)
	}
	if err := h.validateRole(config.RoleConfig{Name: "新角色", Enabled: true}); err != nil {
		t.Fatalf("an unbound role must be accepted: %v", err)
	}
	err := h.validateRole(config.RoleConfig{Name: "新角色", Enabled: true, AIChannel: "nope"})
	if err == nil {
		t.Fatal("an unknown channel must be refused at save time; at run time it silently degrades to the default")
	}
	// The display name is accepted because resolution normalises it the same way.
	if err := h.validateRole(config.RoleConfig{Name: "新角色", Enabled: true, AIChannel: "Deep"}); err != nil {
		t.Fatalf("a channel named by its display form must resolve: %v", err)
	}
}

func TestWriteRoleFileStoresCanonicalChannel(t *testing.T) {
	dir := t.TempDir()
	cfg := roleModelTestConfig()
	cfg.RolesDir = dir
	h := NewRoleHandler(cfg, dir+"/config.yaml", zap.NewNop(), nil, nil)

	if err := h.writeRoleFile(config.RoleConfig{Name: "canonical", Enabled: true, AIChannel: " Deep "}); err != nil {
		t.Fatalf("writeRoleFile: %v", err)
	}
	saved, err := config.LoadRoleFromFile(h.roleFilePath("canonical"))
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if saved.AIChannel != "deep" {
		t.Fatalf("stored ai_channel = %q, want %q: the file must hold a real ai.channels key", saved.AIChannel, "deep")
	}

	// An unbound role must not gain a binding just from being saved.
	if err := h.writeRoleFile(config.RoleConfig{Name: "unbound", Enabled: true}); err != nil {
		t.Fatalf("writeRoleFile(unbound): %v", err)
	}
	saved, err = config.LoadRoleFromFile(h.roleFilePath("unbound"))
	if err != nil {
		t.Fatalf("re-read unbound: %v", err)
	}
	if saved.AIChannel != "" {
		t.Fatalf("stored ai_channel = %q, want empty", saved.AIChannel)
	}
}
