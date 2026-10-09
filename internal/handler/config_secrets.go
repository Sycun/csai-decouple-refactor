package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"cyberstrike-ai/internal/config"
)

const maskedSecret = "********"

func secretConfigKey(key string) bool {
	switch strings.ToLower(key) {
	case "api_key", "apikey", "password", "secret", "client_secret", "app_secret", "token", "bot_token", "app_token", "verify_token", "encoding_aes_key", "authorization", "x-api-key":
		return true
	}
	return false
}

func configSecretSnapshot(cfg *config.Config) GetConfigResponse {
	return GetConfigResponse{AI: cfg.AI, OpenAI: cfg.OpenAI, Vision: cfg.Vision, FOFA: cfg.FOFA,
		ZoomEye: cfg.ZoomEye, Quake: cfg.Quake, Shodan: cfg.Shodan, MCP: cfg.MCP,
		Hitl: cfg.Hitl, Knowledge: cfg.Knowledge, Robots: cfg.Robots}
}

func jsonSecretTree(value interface{}) (interface{}, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var tree interface{}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	err = decoder.Decode(&tree)
	return tree, err
}

// transformSecretTree masks a detached response or restores a submitted marker
// from the same configuration path. It never changes the live configuration.
func transformSecretTree(tree, current interface{}, restoring bool) error {
	switch node := tree.(type) {
	case map[string]interface{}:
		previous, _ := current.(map[string]interface{})
		for key, value := range node {
			text, isString := value.(string)
			if secretConfigKey(key) && isString {
				if !restoring && text != "" {
					node[key] = maskedSecret
				}
				if restoring && text == maskedSecret {
					stored, ok := previous[key].(string)
					if !ok || stored == "" {
						return fmt.Errorf("字段 %s 没有已保存凭据，请填写新的值", key)
					}
					node[key] = stored
				}
				continue
			}
			if err := transformSecretTree(value, previous[key], restoring); err != nil {
				return err
			}
		}
	case []interface{}:
		previous, _ := current.([]interface{})
		for i, value := range node {
			var stored interface{}
			if i < len(previous) {
				stored = previous[i]
			}
			// Named entries can be reordered without moving secrets between them.
			if entry, ok := value.(map[string]interface{}); ok {
				for _, identity := range []string{"id", "name"} {
					if id, ok := entry[identity].(string); ok && id != "" {
						stored = nil
						for _, candidate := range previous {
							if old, ok := candidate.(map[string]interface{}); ok && old[identity] == id {
								stored = old
								break
							}
						}
						break
					}
				}
			}
			if err := transformSecretTree(value, stored, restoring); err != nil {
				return err
			}
		}
	}
	return nil
}

func maskedConfigResponse(response GetConfigResponse) (interface{}, error) {
	tree, err := jsonSecretTree(response)
	if err != nil {
		return nil, err
	}
	err = transformSecretTree(tree, nil, false)
	return tree, err
}

func restoreConfigRequestSecrets(request interface{}, cfg *config.Config) error {
	update, isUpdate := request.(*UpdateConfigRequest)
	mainKeyMasked := isUpdate && update.OpenAI != nil && update.OpenAI.APIKey == maskedSecret
	tree, err := jsonSecretTree(request)
	if err != nil {
		return err
	}
	previous, err := jsonSecretTree(configSecretSnapshot(cfg))
	if err != nil {
		return err
	}
	if err := transformSecretTree(tree, previous, true); err != nil {
		return err
	}
	raw, err := json.Marshal(tree)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, request); err != nil {
		return err
	}
	if mainKeyMasked && update.AI != nil {
		id := config.NormalizeAIChannelID(update.AI.DefaultChannel)
		if channel, ok := update.AI.Channels[id]; ok {
			update.OpenAI.APIKey = channel.APIKey
		}
	}
	return nil
}

// resolveProbeSecret turns a masked key back into the saved one. The caller holds the config read
// lock: this reads the same snapshot the probe entry point is already locking around, and keeping
// it a free function is what keeps it off ConfigHandler's method count.
func resolveProbeSecret(cfg *config.Config, key, channelID, baseURL, scope string) (string, error) {
	if key != maskedSecret {
		return key, nil
	}
	if channelID != "" {
		if ch, ok := cfg.AI.Channels[channelID]; ok && ch.APIKey != "" {
			return ch.APIKey, nil
		}
		return "", fmt.Errorf("通道没有已保存凭据，请填写 API Key")
	}
	var scoped string
	switch scope {
	case "vision":
		scoped = cfg.Vision.APIKey
	case "hitlAudit":
		scoped = cfg.Hitl.AuditModel.APIKey
	case "knowledgeEmbedding":
		scoped = cfg.Knowledge.Embedding.APIKey
	case "openai":
		scoped = cfg.OpenAI.APIKey
	case "":
	default:
		return "", fmt.Errorf("未知凭据范围")
	}
	if scope != "" {
		if scoped == "" {
			scoped = cfg.OpenAI.APIKey
		}
		if scoped != "" {
			return scoped, nil
		}
		return "", fmt.Errorf("没有已保存的 API Key")
	}
	keys := map[string]bool{}
	add := func(url, secret string) {
		if strings.TrimRight(url, "/") == strings.TrimRight(baseURL, "/") && secret != "" {
			keys[secret] = true
		}
	}
	add(cfg.OpenAI.BaseURL, cfg.OpenAI.APIKey)
	for _, ch := range cfg.AI.Channels {
		add(ch.BaseURL, ch.APIKey)
	}
	add(cfg.Hitl.AuditModel.BaseURL, cfg.Hitl.AuditModel.APIKey)
	add(cfg.Vision.BaseURL, cfg.Vision.APIKey)
	add(cfg.Knowledge.Embedding.BaseURL, cfg.Knowledge.Embedding.APIKey)
	add(cfg.Knowledge.Retrieval.Rerank.BaseURL, cfg.Knowledge.Retrieval.Rerank.APIKey)
	if len(keys) == 1 {
		for secret := range keys {
			return secret, nil
		}
	}
	return "", fmt.Errorf("无法唯一确定已保存凭据，请选择通道或填写 API Key")
}
