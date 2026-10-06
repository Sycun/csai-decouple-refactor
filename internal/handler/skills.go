package handler

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"cyberstrike-ai/internal/audit"
	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/skillpackage"
	"cyberstrike-ai/internal/store"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// SkillsHandler Skills处理器（磁盘 + Eino 规范；运行时由 Eino ADK skill 中间件加载）
type SkillsHandler struct {
	config     *config.Config
	configPath string
	logger     *zap.Logger
	stats      *store.SkillStats // skill_stats 表的主人；统计读写都走它
	audit      *audit.Service
}

// SetAudit wires platform audit logging.
func (h *SkillsHandler) SetAudit(s *audit.Service) {
	h.audit = s
}

// NewSkillsHandler 创建新的Skills处理器
func NewSkillsHandler(cfg *config.Config, configPath string, logger *zap.Logger) *SkillsHandler {
	return &SkillsHandler{
		config:     cfg,
		configPath: configPath,
		logger:     logger,
	}
}

func (h *SkillsHandler) skillsRootAbs() string {
	skillsDir := h.config.SkillsDir
	if skillsDir == "" {
		skillsDir = "skills"
	}
	configDir := filepath.Dir(h.configPath)
	if !filepath.IsAbs(skillsDir) {
		skillsDir = filepath.Join(configDir, skillsDir)
	}
	return skillsDir
}

// SetDB points the handler at the skill_stats table.
//
// A nil *database.DB stays a nil store rather than becoming a non-nil interface holding nil: every
// reader below guards the field, and that guard is what makes a server started without a database
// answer "no statistics yet" instead of panicking.
func (h *SkillsHandler) SetDB(db *database.DB) {
	h.stats = newSkillStatsStore(db)
}

// newSkillStatsStore is the one way a handler comes by the skill_stats table: a wiring without a
// database keeps a nil store, whose methods answer an error instead of panicking on a nil receiver.
func newSkillStatsStore(db *database.DB) *store.SkillStats {
	if db == nil {
		return nil
	}
	return store.NewSkillStats(db.DB)
}

// GetSkills 获取所有skills列表（支持分页和搜索）
func (h *SkillsHandler) GetSkills(c *gin.Context) {
	allSummaries, err := h.installedSkillSummaries()
	if err != nil {
		h.logger.Error("获取skills列表失败", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	searchKeyword := strings.TrimSpace(c.Query("search"))

	allSkillsInfo := make([]map[string]interface{}, 0, len(allSummaries))
	for _, s := range allSummaries {
		skillInfo := map[string]interface{}{
			"id":           s.ID,
			"name":         s.Name,
			"dir_name":     s.DirName,
			"description":  s.Description,
			"version":      s.Version,
			"path":         s.Path,
			"tags":         s.Tags,
			"triggers":     s.Triggers,
			"script_count": s.ScriptCount,
			"file_count":   s.FileCount,
			"progressive":  s.Progressive,
			"file_size":    s.FileSize,
			"mod_time":     s.ModTime,
		}
		allSkillsInfo = append(allSkillsInfo, skillInfo)
	}

	filteredSkillsInfo := allSkillsInfo
	if searchKeyword != "" {
		keywordLower := strings.ToLower(searchKeyword)
		filteredSkillsInfo = make([]map[string]interface{}, 0)
		for _, skillInfo := range allSkillsInfo {
			id := strings.ToLower(fmt.Sprintf("%v", skillInfo["id"]))
			name := strings.ToLower(fmt.Sprintf("%v", skillInfo["name"]))
			description := strings.ToLower(fmt.Sprintf("%v", skillInfo["description"]))
			path := strings.ToLower(fmt.Sprintf("%v", skillInfo["path"]))
			version := strings.ToLower(fmt.Sprintf("%v", skillInfo["version"]))
			tagsJoined := ""
			if tags, ok := skillInfo["tags"].([]string); ok {
				tagsJoined = strings.ToLower(strings.Join(tags, " "))
			}
			trigJoined := ""
			if tr, ok := skillInfo["triggers"].([]string); ok {
				trigJoined = strings.ToLower(strings.Join(tr, " "))
			}
			if strings.Contains(id, keywordLower) ||
				strings.Contains(name, keywordLower) ||
				strings.Contains(description, keywordLower) ||
				strings.Contains(path, keywordLower) ||
				strings.Contains(version, keywordLower) ||
				strings.Contains(tagsJoined, keywordLower) ||
				strings.Contains(trigJoined, keywordLower) {
				filteredSkillsInfo = append(filteredSkillsInfo, skillInfo)
			}
		}
	}

	// 分页参数
	limit := 20 // 默认每页20条
	offset := 0
	if limitStr := c.Query("limit"); limitStr != "" {
		if parsed, err := parseInt(limitStr); err == nil && parsed > 0 {
			// 允许更大的limit用于搜索场景，但设置一个合理的上限（10000）
			if parsed <= 10000 {
				limit = parsed
			} else {
				limit = 10000
			}
		}
	}
	if offsetStr := c.Query("offset"); offsetStr != "" {
		if parsed, err := parseInt(offsetStr); err == nil && parsed >= 0 {
			offset = parsed
		}
	}

	// 计算分页范围
	total := len(filteredSkillsInfo)
	start := offset
	end := offset + limit
	if start > total {
		start = total
	}
	if end > total {
		end = total
	}

	// 获取当前页的skill列表
	var paginatedSkillsInfo []map[string]interface{}
	if start < end {
		paginatedSkillsInfo = filteredSkillsInfo[start:end]
	} else {
		paginatedSkillsInfo = []map[string]interface{}{}
	}

	c.JSON(http.StatusOK, gin.H{
		"skills": paginatedSkillsInfo,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

// GetSkill 获取单个skill的详细信息
func (h *SkillsHandler) GetSkill(c *gin.Context) {
	skillName := c.Param("name")
	if skillName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "skill名称不能为空"})
		return
	}

	resPath := strings.TrimSpace(c.Query("resource_path"))
	if resPath == "" {
		resPath = strings.TrimSpace(c.Query("skill_script_path"))
	}
	if resPath != "" {
		scriptRoot, scriptDir := h.skillLocation(skillName)
		content, err := skillpackage.ReadScriptText(scriptRoot, scriptDir, resPath, 0)
		if err != nil {
			h.logger.Warn("读取skill资源失败", zap.String("skill", skillName), zap.String("path", resPath), zap.Error(err))
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"skill": map[string]interface{}{
				"id": skillName,
			},
			"resource": map[string]interface{}{
				"path":    resPath,
				"content": content,
			},
		})
		return
	}

	depthStr := strings.ToLower(strings.TrimSpace(c.DefaultQuery("depth", "full")))
	section := strings.TrimSpace(c.Query("section"))
	opt := skillpackage.LoadOptions{Section: section}
	switch depthStr {
	case "summary":
		opt.Depth = "summary"
	case "full", "":
		opt.Depth = "full"
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "depth 仅支持 summary 或 full"})
		return
	}

	loadRoot, loadDir := h.skillLocation(skillName)
	skill, err := skillpackage.LoadSkill(loadRoot, loadDir, opt)
	if err != nil {
		h.logger.Warn("加载skill失败", zap.String("skill", skillName), zap.Error(err))
		c.JSON(http.StatusNotFound, gin.H{"error": "skill不存在: " + err.Error()})
		return
	}

	skillPath := skill.Path
	skillFile := filepath.Join(skillPath, "SKILL.md")

	fileInfo, _ := os.Stat(skillFile)
	var fileSize int64
	var modTime string
	if fileInfo != nil {
		fileSize = fileInfo.Size()
		modTime = fileInfo.ModTime().Format("2006-01-02 15:04:05")
	}

	c.JSON(http.StatusOK, gin.H{
		"skill": map[string]interface{}{
			"id":            skill.DirName,
			"name":          skill.Name,
			"description":   skill.Description,
			"content":       skill.Content,
			"path":          skill.Path,
			"version":       skill.Version,
			"tags":          skill.Tags,
			"scripts":       skill.Scripts,
			"sections":      skill.Sections,
			"package_files": skill.PackageFiles,
			"file_size":     fileSize,
			"mod_time":      modTime,
			"depth":         depthStr,
			"section":       section,
		},
	})
}

// ListSkillPackageFiles lists all files in a skill directory (Agent Skills layout).
func (h *SkillsHandler) ListSkillPackageFiles(c *gin.Context) {
	skillID := c.Param("name")
	listRoot, listDir := h.skillLocation(skillID)
	files, err := skillpackage.ListPackageFiles(listRoot, listDir)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"files": files})
}

// GetSkillPackageFile returns one file by relative path (?path=).
func (h *SkillsHandler) GetSkillPackageFile(c *gin.Context) {
	skillID := c.Param("name")
	rel := strings.TrimSpace(c.Query("path"))
	if rel == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "query path is required"})
		return
	}
	readRoot, readDir := h.skillLocation(skillID)
	b, err := skillpackage.ReadPackageFile(readRoot, readDir, rel, 0)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"path": rel, "content": string(b)})
}

// PutSkillPackageFile writes a file inside the skill package.
func (h *SkillsHandler) PutSkillPackageFile(c *gin.Context) {
	skillID := c.Param("name")
	var req struct {
		Path    string `json:"path" binding:"required"`
		Content string `json:"content"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的请求参数: " + err.Error()})
		return
	}
	if msg, refused := h.refuseBundleSkill(skillID); refused {
		c.JSON(http.StatusConflict, gin.H{"error": msg})
		return
	}
	if req.Path == "SKILL.md" {
		if err := skillpackage.ValidateSkillMDPackage([]byte(req.Content), skillID); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}
	writeRoot, writeDir := h.skillLocation(skillID)
	if err := skillpackage.WritePackageFile(writeRoot, writeDir, req.Path, []byte(req.Content)); err != nil {
		h.logger.Error("写入 skill 文件失败", zap.String("skill", skillID), zap.String("path", req.Path), zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "saved", "path": req.Path})
}

// GetSkillBoundRoles 获取绑定指定skill的角色列表
func (h *SkillsHandler) GetSkillBoundRoles(c *gin.Context) {
	skillName := c.Param("name")
	if skillName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "skill名称不能为空"})
		return
	}

	boundRoles := h.getRolesBoundToSkill(skillName)
	c.JSON(http.StatusOK, gin.H{
		"skill":       skillName,
		"bound_roles": boundRoles,
		"bound_count": len(boundRoles),
	})
}

// getRolesBoundToSkill 预留：角色不再配置 skill 绑定，始终返回空列表。
func (h *SkillsHandler) getRolesBoundToSkill(skillName string) []string {
	_ = skillName
	return nil
}

// CreateSkill 创建新 skill（标准 Agent Skills：生成 SKILL.md + YAML front matter）
func (h *SkillsHandler) CreateSkill(c *gin.Context) {
	var req struct {
		Name        string `json:"name" binding:"required"`
		Description string `json:"description" binding:"required"`
		Content     string `json:"content" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的请求参数: " + err.Error()})
		return
	}

	if !isValidSkillName(req.Name) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "skill 目录名须为小写字母、数字、连字符（与 Agent Skills name 一致）"})
		return
	}

	manifest := &skillpackage.SkillManifest{
		Name:        req.Name,
		Description: strings.TrimSpace(req.Description),
	}
	skillMD, err := skillpackage.BuildSkillMD(manifest, req.Content)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := skillpackage.ValidateSkillMDPackage(skillMD, req.Name); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if msg, refused := h.refuseBundleSkill(req.Name); refused {
		c.JSON(http.StatusConflict, gin.H{"error": msg})
		return
	}
	// Created skills always land in the built-in directory: a bundle's directory is not a place
	// this API should be writing new skills into.
	skillDir := filepath.Join(h.skillsRootAbs(), req.Name)
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		h.logger.Error("创建skill目录失败", zap.String("skill", req.Name), zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建skill目录失败: " + err.Error()})
		return
	}

	if _, err := os.Stat(filepath.Join(skillDir, "SKILL.md")); err == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "skill已存在"})
		return
	}

	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), skillMD, 0644); err != nil {
		h.logger.Error("创建 SKILL.md 失败", zap.String("skill", req.Name), zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建 SKILL.md 失败: " + err.Error()})
		return
	}
	// Register it in the same table the listing and the run path read; a skill that exists only
	// on disk would disappear from GET /api/skills the moment a table is installed.
	if err := h.putSkillUnit(req.Name); err != nil {
		h.logger.Error("创建 skill 后登记到能力表失败", zap.String("skill", req.Name), zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	h.logger.Info("创建skill成功", zap.String("skill", req.Name))
	if h.audit != nil {
		h.audit.RecordOK(c, "skill", "create", "创建 Skill", "skill", req.Name, nil)
	}
	c.JSON(http.StatusOK, gin.H{
		"message": "skill已创建",
		"skill": map[string]interface{}{
			"name": req.Name,
			"path": skillDir,
		},
	})
}

// UpdateSkill 更新 SKILL.md（保留 front matter 中除 description 外的字段；可选覆盖 description）
func (h *SkillsHandler) UpdateSkill(c *gin.Context) {
	skillName := c.Param("name")
	if skillName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "skill名称不能为空"})
		return
	}

	var req struct {
		Description string `json:"description"`
		Content     string `json:"content" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的请求参数: " + err.Error()})
		return
	}

	if msg, refused := h.refuseBundleSkill(skillName); refused {
		c.JSON(http.StatusConflict, gin.H{"error": msg})
		return
	}
	updateRoot, updateDir := h.skillLocation(skillName)
	mdPath := filepath.Join(updateRoot, updateDir, "SKILL.md")
	raw, err := os.ReadFile(mdPath)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "skill不存在: " + err.Error()})
		return
	}
	m, _, err := skillpackage.ParseSkillMD(raw)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Description != "" {
		m.Description = strings.TrimSpace(req.Description)
	}
	skillMD, err := skillpackage.BuildSkillMD(m, req.Content)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := skillpackage.ValidateSkillMDPackage(skillMD, skillName); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	skillDir := filepath.Join(updateRoot, updateDir)

	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), skillMD, 0644); err != nil {
		h.logger.Error("更新 SKILL.md 失败", zap.String("skill", skillName), zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "更新 SKILL.md 失败: " + err.Error()})
		return
	}

	h.logger.Info("更新skill成功", zap.String("skill", skillName))
	if h.audit != nil {
		h.audit.RecordOK(c, "skill", "update", "更新 Skill", "skill", skillName, nil)
	}
	c.JSON(http.StatusOK, gin.H{
		"message": "skill已更新",
	})
}

// DeleteSkill 删除skill
func (h *SkillsHandler) DeleteSkill(c *gin.Context) {
	skillName := c.Param("name")
	if skillName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "skill名称不能为空"})
		return
	}

	// 检查是否有角色绑定了该skill，如果有则自动移除绑定
	affectedRoles := h.removeSkillFromRoles(skillName)
	if len(affectedRoles) > 0 {
		h.logger.Info("从角色中移除skill绑定",
			zap.String("skill", skillName),
			zap.Strings("roles", affectedRoles))
	}

	if msg, refused := h.refuseBundleSkill(skillName); refused {
		c.JSON(http.StatusConflict, gin.H{"error": msg})
		return
	}
	deleteRoot, deleteDir := h.skillLocation(skillName)
	skillDir := filepath.Join(deleteRoot, deleteDir)
	if err := os.RemoveAll(skillDir); err != nil {
		h.logger.Error("删除skill失败", zap.String("skill", skillName), zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "删除skill失败: " + err.Error()})
		return
	}
	if err := h.removeSkillUnit(skillName); err != nil {
		h.logger.Error("删除 skill 后能力表未同步", zap.String("skill", skillName), zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	responseMsg := "skill已删除"
	if len(affectedRoles) > 0 {
		responseMsg = fmt.Sprintf("skill已删除，已自动从 %d 个角色中移除绑定: %s",
			len(affectedRoles), strings.Join(affectedRoles, ", "))
	}

	h.logger.Info("删除skill成功", zap.String("skill", skillName))
	if h.audit != nil {
		h.audit.RecordOK(c, "skill", "delete", "删除 Skill", "skill", skillName, map[string]interface{}{
			"affected_roles": affectedRoles,
		})
	}
	c.JSON(http.StatusOK, gin.H{
		"message":        responseMsg,
		"affected_roles": affectedRoles,
	})
}

// GetSkillStats 获取skills调用统计信息
func (h *SkillsHandler) GetSkillStats(c *gin.Context) {
	skillList, err := h.installedSkillDirNames()
	if err != nil {
		h.logger.Error("获取skills列表失败", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	skillsDir := h.skillsRootAbs()

	// 从数据库加载调用统计
	var skillStatsMap map[string]*store.SkillCallStats
	if h.stats != nil {
		dbStats, err := h.stats.Load()
		if err != nil {
			h.logger.Warn("从数据库加载Skills统计信息失败", zap.Error(err))
			skillStatsMap = make(map[string]*store.SkillCallStats)
		} else {
			skillStatsMap = dbStats
		}
	} else {
		skillStatsMap = make(map[string]*store.SkillCallStats)
	}

	// 构建统计信息（包含所有skills，即使没有调用记录）
	statsList := make([]map[string]interface{}, 0, len(skillList))
	totalCalls := 0
	totalSuccess := 0
	totalFailed := 0

	for _, skillName := range skillList {
		stat, exists := skillStatsMap[skillName]
		if !exists {
			stat = &store.SkillCallStats{
				SkillName:    skillName,
				TotalCalls:   0,
				SuccessCalls: 0,
				FailedCalls:  0,
			}
		}

		totalCalls += stat.TotalCalls
		totalSuccess += stat.SuccessCalls
		totalFailed += stat.FailedCalls

		lastCallTimeStr := ""
		if stat.LastCallTime != nil {
			lastCallTimeStr = stat.LastCallTime.Format("2006-01-02 15:04:05")
		}

		statsList = append(statsList, map[string]interface{}{
			"skill_name":     stat.SkillName,
			"total_calls":    stat.TotalCalls,
			"success_calls":  stat.SuccessCalls,
			"failed_calls":   stat.FailedCalls,
			"last_call_time": lastCallTimeStr,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"total_skills":  len(skillList),
		"total_calls":   totalCalls,
		"total_success": totalSuccess,
		"total_failed":  totalFailed,
		"skills_dir":    skillsDir,
		"stats":         statsList,
	})
}

// ClearSkillStats 清空所有Skills统计信息
func (h *SkillsHandler) ClearSkillStats(c *gin.Context) {
	if h.stats == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "数据库连接未配置"})
		return
	}

	if err := h.stats.Clear(); err != nil {
		h.logger.Error("清空Skills统计信息失败", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "清空统计信息失败: " + err.Error()})
		return
	}

	h.logger.Info("已清空所有Skills统计信息")
	c.JSON(http.StatusOK, gin.H{
		"message": "已清空所有Skills统计信息",
	})
}

// ClearSkillStatsByName 清空指定skill的统计信息
func (h *SkillsHandler) ClearSkillStatsByName(c *gin.Context) {
	skillName := c.Param("name")
	if skillName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "skill名称不能为空"})
		return
	}

	if h.stats == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "数据库连接未配置"})
		return
	}

	if err := h.stats.ClearSkill(skillName); err != nil {
		h.logger.Error("清空指定skill统计信息失败", zap.String("skill", skillName), zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "清空统计信息失败: " + err.Error()})
		return
	}

	h.logger.Info("已清空指定skill统计信息", zap.String("skill", skillName))
	c.JSON(http.StatusOK, gin.H{
		"message": fmt.Sprintf("已清空skill '%s' 的统计信息", skillName),
	})
}

// removeSkillFromRoles 预留：角色不再存储 skill 绑定，无操作。
func (h *SkillsHandler) removeSkillFromRoles(skillName string) []string {
	_ = skillName
	return nil
}

// isValidSkillName 验证 skill 目录名（与 Agent Skills 的 name 字段一致：小写、数字、连字符）
func isValidSkillName(name string) bool {
	if name == "" || len(name) > 100 {
		return false
	}
	for _, r := range name {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-') {
			return false
		}
	}
	return true
}
