package handler

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"cyberstrike-ai/internal/audit"
	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/plugin"
	"cyberstrike-ai/internal/store"

	"gopkg.in/yaml.v3"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// RoleHandler 角色处理器
//
// plugins is the capability table the catalog is rebuilt from on every change; see
// role_catalog.go for why the directory scan and the published snapshot are separate steps.
type RoleHandler struct {
	config     *config.Config
	configPath string
	logger     *zap.Logger
	audit      *audit.Service
	plugins    *plugin.Table
	workflows  *store.Workflows
}

// SetAudit wires platform audit logging.
func (h *RoleHandler) SetAudit(s *audit.Service) {
	h.audit = s
}

// NewRoleHandler 创建新的角色处理器。workflows 是角色绑定校验要读的存储（见 role_validation.go）；
// 它是构造参数而不是 setter，因为一个少了它的 RoleHandler 会在校验绑定时报"存储不可用"。
func NewRoleHandler(cfg *config.Config, configPath string, logger *zap.Logger, plugins *plugin.Table, workflows *store.Workflows) *RoleHandler {
	return &RoleHandler{
		config:     cfg,
		configPath: configPath,
		logger:     logger,
		plugins:    plugins,
		workflows:  workflows,
	}
}

// GetRoles 获取所有角色
func (h *RoleHandler) GetRoles(c *gin.Context) {
	// Reading must not write. This handler used to initialise h.config.Roles on a GET,
	// which mutated a map eight other files read without a lock.
	roles := currentRoles(h.config)

	out := make([]config.RoleConfig, 0, len(roles))
	for key, role := range roles {
		// 确保角色的key与name一致
		if role.Name == "" {
			role.Name = key
		}
		out = append(out, role)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	c.JSON(http.StatusOK, gin.H{
		"roles": out,
	})
}

// GetRole 获取单个角色
func (h *RoleHandler) GetRole(c *gin.Context) {
	roleName := c.Param("name")
	if roleName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "角色名称不能为空"})
		return
	}

	roles := currentRoles(h.config)
	role, exists := roles[roleName]
	if !exists {
		c.JSON(http.StatusNotFound, gin.H{"error": "角色不存在"})
		return
	}

	// 确保角色的name与key一致
	if role.Name == "" {
		role.Name = roleName
	}

	c.JSON(http.StatusOK, gin.H{
		"role": role,
	})
}

// UpdateRole 更新角色
func (h *RoleHandler) UpdateRole(c *gin.Context) {
	roleName := c.Param("name")
	if roleName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "角色名称不能为空"})
		return
	}

	var req config.RoleConfig
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的请求参数: " + err.Error()})
		return
	}

	// 确保角色名称与请求中的name一致
	if req.Name == "" {
		req.Name = roleName
	}
	if err := h.validateRole(req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Name != roleName {
		if _, exists := h.config.Roles[req.Name]; exists {
			c.JSON(http.StatusConflict, gin.H{"error": "角色已存在"})
			return
		}
	}

	roles := currentRoles(h.config)
	if _, exists := roles[roleName]; !exists {
		c.JSON(http.StatusNotFound, gin.H{"error": "角色不存在"})
		return
	}
	if bundle, owned := h.bundleOwnedRole(roleName); owned {
		c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("角色 %q 由能力包 %q 提供，不能在这里修改", roleName, bundle)})
		return
	}
	// Renaming onto an existing identity is refused rather than merged: the old code deleted
	// every entry whose name matched, which could take a *bundle's* role down with it.
	if req.Name != roleName {
		if _, taken := roles[req.Name]; taken {
			c.JSON(http.StatusBadRequest, gin.H{"error": "角色名称已存在"})
			return
		}
		if bundle, owned := h.bundleOwnedRole(req.Name); owned {
			c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("角色 %q 由能力包 %q 提供，不能被占用", req.Name, bundle)})
			return
		}
	}

	if req.Name != roleName {
		if err := h.removeRoleUnit(roleName); err != nil {
			h.roleMutationFailed(c, roleName, err)
			return
		}
		if err := h.deleteRoleFiles(roleName); err != nil {
			h.logger.Warn("删除旧角色配置文件失败", zap.String("role", roleName), zap.Error(err))
		}
	}

	if err := h.writeRoleFile(req); err != nil {
		h.logger.Error("保存配置失败", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存配置失败: " + err.Error()})
		return
	}
	if err := h.putRoleUnit(req.Name, h.roleFilePath(req.Name)); err != nil {
		h.roleMutationFailed(c, req.Name, err)
		return
	}
	if _, err := h.publishRoles(); err != nil {
		h.roleMutationFailed(c, req.Name, err)
		return
	}

	h.logger.Info("更新角色", zap.String("oldKey", roleName), zap.String("newKey", req.Name), zap.String("name", req.Name))
	if h.audit != nil {
		h.audit.RecordOK(c, "role", "update", "更新角色", "role", req.Name, map[string]interface{}{"name": req.Name})
	}
	c.JSON(http.StatusOK, gin.H{
		"message": "角色已更新",
		"role":    req,
	})
}

// roleMutationFailed answers one error the way the plug-in layer means it: a refusal naming the
// owning bundle is the caller's conflict, anything else is a server fault.
func (h *RoleHandler) roleMutationFailed(c *gin.Context, name string, err error) {
	if err == nil {
		return
	}
	var conflict *plugin.ErrConflict
	if errors.As(err, &conflict) {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	if strings.Contains(err.Error(), "由能力包") {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	h.logger.Error("角色变更失败", zap.String("role", name), zap.Error(err))
	c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
}

// CreateRole 创建新角色
func (h *RoleHandler) CreateRole(c *gin.Context) {
	var req config.RoleConfig
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的请求参数: " + err.Error()})
		return
	}

	if err := h.validateRole(req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	roles := currentRoles(h.config)
	if _, exists := roles[req.Name]; exists {
		c.JSON(http.StatusBadRequest, gin.H{"error": "角色已存在"})
		return
	}
	if bundle, owned := h.bundleOwnedRole(req.Name); owned {
		c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("角色 %q 由能力包 %q 提供，不能重复创建", req.Name, bundle)})
		return
	}

	// 创建角色（默认启用）
	if !req.Enabled {
		req.Enabled = true
	}

	if err := h.writeRoleFile(req); err != nil {
		h.logger.Error("保存配置失败", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存配置失败: " + err.Error()})
		return
	}
	if err := h.putRoleUnit(req.Name, h.roleFilePath(req.Name)); err != nil {
		h.roleMutationFailed(c, req.Name, err)
		return
	}
	if _, err := h.publishRoles(); err != nil {
		h.roleMutationFailed(c, req.Name, err)
		return
	}

	h.logger.Info("创建角色", zap.String("roleName", req.Name))
	if h.audit != nil {
		h.audit.RecordOK(c, "role", "create", "创建角色", "role", req.Name, nil)
	}
	c.JSON(http.StatusOK, gin.H{
		"message": "角色已创建",
		"role":    req,
	})
}

// DeleteRole 删除角色
func (h *RoleHandler) DeleteRole(c *gin.Context) {
	roleName := c.Param("name")
	if roleName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "角色名称不能为空"})
		return
	}

	if _, exists := currentRoles(h.config)[roleName]; !exists {
		c.JSON(http.StatusNotFound, gin.H{"error": "角色不存在"})
		return
	}

	// 不允许删除"默认"角色
	if roleName == "默认" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "不能删除默认角色"})
		return
	}
	if bundle, owned := h.bundleOwnedRole(roleName); owned {
		c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("角色 %q 由能力包 %q 提供，请卸载该包而不是在此删除", roleName, bundle)})
		return
	}

	// Unit first, then files: if the unit removal is refused, the role is still on disk and
	// still consistent; the reverse order would leave a file no catalog entry points at.
	if err := h.removeRoleUnit(roleName); err != nil {
		h.roleMutationFailed(c, roleName, err)
		return
	}
	if err := h.deleteRoleFiles(roleName); err != nil {
		h.logger.Warn("删除角色配置文件失败", zap.String("role", roleName), zap.Error(err))
	}
	if _, err := h.publishRoles(); err != nil {
		h.roleMutationFailed(c, roleName, err)
		return
	}

	h.logger.Info("删除角色", zap.String("roleName", roleName))
	if h.audit != nil {
		h.audit.RecordOK(c, "role", "delete", "删除角色", "role", roleName, nil)
	}
	c.JSON(http.StatusOK, gin.H{
		"message": "角色已删除",
	})
}

// roleFilePath is where one role lives on disk. Names are sanitized the same way the shipped
// files are, so a role created through the API is found again by the directory scan.
func (h *RoleHandler) roleFilePath(name string) string {
	return filepath.Join(h.rolesDir(), sanitizeFileName(name)+".yaml")
}

// writeRoleFile saves exactly one role. It used to be "marshal the whole map back to disk",
// which had a consequence nobody intended: once a bundle installed a role, the next unrelated
// edit would copy that bundle's role into the built-in roles/ directory too.
func (h *RoleHandler) writeRoleFile(role config.RoleConfig) error {
	rolesDir := h.rolesDir()
	if err := os.MkdirAll(rolesDir, 0o755); err != nil {
		return fmt.Errorf("创建角色目录失败: %w", err)
	}
	if strings.TrimSpace(role.Name) == "" {
		return fmt.Errorf("角色名称不能为空")
	}

	roleData, err := yaml.Marshal(&role)
	if err != nil {
		return fmt.Errorf("序列化角色配置失败: %w", err)
	}
	// The icon field needs quoting for a \U escape to survive a re-read, exactly as before.
	if role.Icon != "" && strings.HasPrefix(role.Icon, "\\U") {
		re := regexp.MustCompile(`(?m)^(icon:\s+)(\\U[0-9A-F]{8})(\s*)$`)
		roleData = []byte(re.ReplaceAllString(string(roleData), `${1}"${2}"${3}`))
	}

	path := h.roleFilePath(role.Name)
	if err := os.WriteFile(path, roleData, 0o644); err != nil {
		return fmt.Errorf("保存角色配置文件失败: %w", err)
	}
	h.logger.Info("角色配置已保存到文件", zap.String("role", role.Name), zap.String("file", path))
	return nil
}

// deleteRoleFiles removes the built-in file(s) for one role, including the legacy .yml form.
func (h *RoleHandler) deleteRoleFiles(name string) error {
	stem := sanitizeFileName(name)
	var firstErr error
	for _, ext := range []string{".yaml", ".yml"} {
		path := filepath.Join(h.rolesDir(), stem+ext)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		if err := os.Remove(path); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("删除角色配置文件 %s: %w", path, err)
			continue
		}
		h.logger.Info("已删除角色配置文件", zap.String("file", path))
	}
	return firstErr
}

// sanitizeFileName 将角色名称转换为安全的文件名
func sanitizeFileName(name string) string {
	// 替换可能不安全的字符
	replacer := map[rune]string{
		'/':  "_",
		'\\': "_",
		':':  "_",
		'*':  "_",
		'?':  "_",
		'"':  "_",
		'<':  "_",
		'>':  "_",
		'|':  "_",
		' ':  "_",
	}

	var result []rune
	for _, r := range name {
		if replacement, ok := replacer[r]; ok {
			result = append(result, []rune(replacement)...)
		} else {
			result = append(result, r)
		}
	}

	fileName := string(result)
	// 如果文件名为空，使用默认名称
	if fileName == "" {
		fileName = "role"
	}

	return fileName
}

// updateRolesConfig 更新角色配置
func updateRolesConfig(doc *yaml.Node, cfg config.RolesConfig) {
	root := doc.Content[0]
	rolesNode := ensureMap(root, "roles")

	// 清空现有角色
	if rolesNode.Kind == yaml.MappingNode {
		rolesNode.Content = nil
	}

	// 添加新角色（使用name作为key，确保唯一性）
	if cfg.Roles != nil {
		// 先建立一个以name为key的map，去重（保留最后一个）
		rolesByName := make(map[string]config.RoleConfig)
		for roleKey, role := range cfg.Roles {
			// 确保角色的name字段正确设置
			if role.Name == "" {
				role.Name = roleKey
			}
			// 使用name作为最终key，如果有多个key对应相同的name，只保留最后一个
			rolesByName[role.Name] = role
		}

		// 将去重后的角色写入YAML
		for roleName, role := range rolesByName {
			roleNode := ensureMap(rolesNode, roleName)
			setStringInMap(roleNode, "name", role.Name)
			setStringInMap(roleNode, "description", role.Description)
			setStringInMap(roleNode, "user_prompt", role.UserPrompt)
			if role.Icon != "" {
				setStringInMap(roleNode, "icon", role.Icon)
			}
			setBoolInMap(roleNode, "enabled", role.Enabled)

			// 添加工具列表（优先使用tools字段）
			if len(role.Tools) > 0 {
				toolsNode := ensureArray(roleNode, "tools")
				toolsNode.Content = nil
				for _, toolKey := range role.Tools {
					toolNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: toolKey}
					toolsNode.Content = append(toolsNode.Content, toolNode)
				}
			} else if len(role.MCPs) > 0 {
				// 向后兼容：如果没有tools但有mcps，保存mcps
				mcpsNode := ensureArray(roleNode, "mcps")
				mcpsNode.Content = nil
				for _, mcpName := range role.MCPs {
					mcpNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: mcpName}
					mcpsNode.Content = append(mcpsNode.Content, mcpNode)
				}
			}
		}
	}
}

// ensureArray 确保数组中存在指定key的数组节点
func ensureArray(parent *yaml.Node, key string) *yaml.Node {
	_, valueNode := ensureKeyValue(parent, key)
	if valueNode.Kind != yaml.SequenceNode {
		valueNode.Kind = yaml.SequenceNode
		valueNode.Tag = "!!seq"
		valueNode.Content = nil
	}
	return valueNode
}
