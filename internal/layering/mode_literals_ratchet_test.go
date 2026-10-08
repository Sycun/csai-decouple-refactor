package layering

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestAgentModeIdentityLiteralsOnlyShrink —— 对话模式的身份字面量在「声明地与消费地」
// 之外只准降。
//
// 背景：模式身份曾散在至少五份归一化实现里（config 两份、store、robot、workflow 内联），
// 别名集互不一致——"pe" 走会话路径会静默变成 eino_single。收口后（internal/agentmode
// 是唯一声明），模式名的字符串字面量只剩两类合法出现：
//
//   - internal/agentmode：声明表本体（单一来源）；
//   - internal/multiagent：引擎实现按编排名分派，是消费方不是声明方。
//
// 其余每一处都是「又抄了一份清单」的种子：OpenAPI 描述里的枚举、markdown 加载器的
// kind 识别、管理面渲染。它们不回潮的判据不是注释，是这张上限表——上限是收口当天的
// 实测账（OpenAPI 6 + markdown 4），只降不升；谁再往别处写一个字面量就会红。
//
// 口径：恰好等于 "eino_single" 或 "plan_execute" 的字符串字面量（两词在模式域外独有；
// "deep"/"supervisor" 是普通英文词，混进来只会制造误报）。测试文件与注释不计。
func TestAgentModeIdentityLiteralsOnlyShrink(t *testing.T) {
	root := moduleRoot(t)
	identities := map[string]bool{"eino_single": true, "plan_execute": true}
	exemptDirs := []string{
		filepath.Join("internal", "agentmode"),
		filepath.Join("internal", "multiagent"),
	}
	counts := map[string]int{}
	fset := token.NewFileSet()
	walkErr := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		for _, dir := range exemptDirs {
			if strings.HasPrefix(rel, dir+string(filepath.Separator)) {
				return nil
			}
		}
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			v, unqErr := strconv.Unquote(lit.Value)
			if unqErr == nil && identities[v] {
				counts[filepath.ToSlash(rel)]++
			}
			return true
		})
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk internal/: %v", walkErr)
	}

	// 上限 = 收口当天的实测账，只降不升。未列出的文件必须为 0。
	ceilings := map[string]int{
		"internal/handler/openapi_paths_chat.go": 4,
		"internal/handler/openapi_components.go": 2,
		"internal/agents/markdown.go":            3,
		"internal/handler/markdown_agents.go":    1,
	}

	var regressions []string
	total := 0
	for file, n := range counts {
		total += n
		limit, known := ceilings[file]
		if !known {
			regressions = append(regressions, file+": "+strconv.Itoa(n)+" 处身份字面量（不在账上）")
			continue
		}
		if n > limit {
			regressions = append(regressions, file+": "+strconv.Itoa(n)+" > ceiling "+strconv.Itoa(limit))
		}
	}
	for file, limit := range ceilings {
		if counts[file] < limit {
			t.Logf("%s 降到 %d 处（ceiling %d）：该收紧 ceilings", file, counts[file], limit)
		}
	}
	sort.Strings(regressions)
	if len(regressions) > 0 {
		t.Fatalf("模式身份字面量超出账本——模式清单与别名只允许在 internal/agentmode 声明"+
			"（引擎实现 internal/multiagent 除外）；要引用清单请用 agentmode 的导出常量：\n%s",
			strings.Join(regressions, "\n"))
	}
	if total == 0 {
		t.Fatalf("一个身份字面量都没数到——扫描器坏了，不是账干净了")
	}
}
