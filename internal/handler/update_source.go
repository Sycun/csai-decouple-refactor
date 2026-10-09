package handler

import (
	"strings"
	"sync"

	"cyberstrike-ai/internal/config"
)

// UpdateSourceStore 拥有一键更新的更新源（config.yaml 的 update 段）这笔状态：页面读它、
// 保存也走它，落盘复用 ConfigHandler 的 saveConfig，因此文件仍只有一处写入。
//
// 做成自带锁的协作件而不是往 ConfigHandler 上再挂两个方法：那个类型已是全站最大的门脸
// （见 internal/layering 的棘轮），新能力该长在有自己状态的协作件上。
type UpdateSourceStore struct {
	mu   sync.RWMutex
	cfg  *config.UpdateConfig
	save func() error
}

func newUpdateSourceStore(cfg *config.UpdateConfig, save func() error) *UpdateSourceStore {
	return &UpdateSourceStore{cfg: cfg, save: save}
}

// Get 读当前生效的更新源仓库地址；空 = 官方仓库，跟随其默认分支。
// 更新处理器每次请求现读，保存后立即生效。
func (s *UpdateSourceStore) Get() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.Repo
}

// Save 记录 update.repo 并落盘；留空即回到官方仓库。校验在端点层（复用与更新同一套白名单），
// 这里只负责把值送进文件——落盘失败就把内存里的值退回原样，不留下"页面显示已改、文件没改"。
func (s *UpdateSourceStore) Save(repo string) error {
	s.mu.Lock()
	previous := *s.cfg
	*s.cfg = config.UpdateConfig{Repo: strings.TrimSpace(repo)}
	s.mu.Unlock()
	if err := s.save(); err != nil {
		s.mu.Lock()
		*s.cfg = previous
		s.mu.Unlock()
		return err
	}
	return nil
}
