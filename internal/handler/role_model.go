package handler

import (
	"errors"
	"fmt"
	"strings"

	"cyberstrike-ai/internal/config"

	"go.uber.org/zap"
)

// "One role, one model" is expressed as a role binding itself to an AI channel, because a
// channel already carries provider / base_url / api_key / model / reasoning. A role therefore
// names a model instead of duplicating credentials, and an operator who swaps a channel's model
// swaps it for every role pointing at it.
//
// Precedence for a run, highest first:
//
//	1. the channel the caller named for this session (ChatRequest.aiChannelId)
//	2. the channel the role binds (roles/<name>.yaml -> ai_channel)
//	3. ai.default_channel
//
// (1) beating (2) is what keeps the chat page's channel picker meaningful. It is safe because
// the picker only puts aiChannelId in the request body when the user actually chose a channel:
// "follow the default" is selected as an empty value and sent as nothing at all (chat.js
// selectedChatAIChannelId -> body.aiChannelId), so a non-empty value is always a deliberate
// override rather than an echo of the default.

// normalizeRoleAIChannel canonicalises a role's channel binding for storage.
//
// Empty must stay empty: config.NormalizeAIChannelID maps "" to "default", which would pin
// every role that means "follow the global default" to a channel literally named default.
func normalizeRoleAIChannel(id string) string {
	if strings.TrimSpace(id) == "" {
		return ""
	}
	return config.NormalizeAIChannelID(id)
}

// roleAIChannel returns the channel a role binds, or "" when the role follows the global
// default.
//
// A binding that no longer resolves - the channel was renamed or deleted after the role was
// saved, or shipped inside a bundle whose pack assumes a channel this deployment lacks - is
// reported as "" so the run falls back to the default channel instead of failing. Taking a
// conversation down over a stale role file is a worse outcome than running it on the default
// model, and the warning below is the trace that says it happened. validateRole refuses to
// save an unknown channel, which is what keeps this path rare.
func roleAIChannel(fallback *config.Config, roleName string, logger *zap.Logger) string {
	name := strings.TrimSpace(roleName)
	if name == "" {
		return ""
	}
	role, ok := lookupRole(fallback, name)
	if !ok {
		return ""
	}
	id := normalizeRoleAIChannel(role.AIChannel)
	if id == "" {
		return ""
	}
	live := currentConfig(fallback)
	if live == nil {
		return ""
	}
	// AI.ResolveChannel rather than Config.ResolveAIChannel: the latter falls back to the
	// legacy top-level openai block, which would report "found" for a channel that is not in
	// ai.channels at all and hide the stale binding.
	if _, _, found := live.AI.ResolveChannel(id); !found {
		if logger != nil {
			logger.Warn("角色绑定的 AI 通道不存在，本次运行回落默认通道",
				zap.String("role", name),
				zap.String("aiChannel", id),
				zap.String("defaultChannel", config.NormalizeAIChannelID(live.AI.DefaultChannel)))
		}
		return ""
	}
	return id
}

// effectiveAIChannelID resolves the channel one run should use. An empty result means "let
// ResolveAIChannel apply ai.default_channel", which is the behaviour every caller had before
// roles could bind a model.
func effectiveAIChannelID(fallback *config.Config, explicitChannel, roleName string, logger *zap.Logger) string {
	if id := strings.TrimSpace(explicitChannel); id != "" {
		return id
	}
	return roleAIChannel(fallback, roleName, logger)
}

// resolveRunConfig returns a copy of the live snapshot whose OpenAI block is the channel this
// run resolved to. It is a free function rather than an AgentHandler method because the workflow
// paths need the same resolution and do not hang off that type.
//
// The copy matters: everything downstream reads it, so writing the resolved channel into the
// snapshot instead would leak one run's model into every other run in the process.
func resolveRunConfig(fallback *config.Config, explicitChannel, roleName string, logger *zap.Logger) (*config.Config, string, error) {
	live := currentConfig(fallback)
	if live == nil {
		return nil, "", errors.New("服务器配置未加载")
	}
	oa, resolvedID, ok := live.ResolveAIChannel(effectiveAIChannelID(fallback, explicitChannel, roleName, logger))
	if !ok {
		return nil, resolvedID, fmt.Errorf("AI 通道不存在: %s", resolvedID)
	}
	cfgCopy := *live
	cfgCopy.OpenAI = oa
	return &cfgCopy, resolvedID, nil
}

// runConfigForRole is resolveRunConfig for callers that used to run on the raw snapshot and had
// no channel resolution at all - the role-bound workflow paths. Those keep working unchanged when
// nothing resolves, rather than gaining a brand-new way to fail: the only error resolveRunConfig
// can return here is "the default channel itself is unusable", which those paths already surfaced
// later, from inside the model client. A role binding that does not resolve is not an error either;
// roleAIChannel downgrades it to the default and logs.
func runConfigForRole(fallback *config.Config, explicitChannel, roleName string, logger *zap.Logger) *config.Config {
	if cfg, _, err := resolveRunConfig(fallback, explicitChannel, roleName, logger); err == nil && cfg != nil {
		return cfg
	}
	return currentConfig(fallback)
}
