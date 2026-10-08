package agentmode

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// declaration 是激活一个模式单元所需的声明文件形状。文件只携带身份：
// 执行绑定（runner / 编排名）在内核表里，声明改不了它——所以一个包不可能把
// deep 指到别的执行器上，也不可能发明一个内核不会跑的模式。
type declaration struct {
	ID string `yaml:"id"`
}

// ReadDeclarationFile 读取一份声明文件，单元名取文件名（去扩展名）。
func ReadDeclarationFile(path string) (Mode, error) {
	base := filepath.Base(filepath.ToSlash(path))
	if dot := strings.LastIndex(base, "."); dot > 0 {
		base = base[:dot]
	}
	return ReadDeclaration(path, base)
}

// ReadDeclaration 读取并校验一份模式声明。unitName 是该单元在能力表里的名字，
// id 字段必须与之相同：防「拷贝 supervisor.yaml 改名成 deep.yaml、内容忘了改」
// 这类复制粘贴错——两个名字在同一个文件里对质，错的那个进不了表。
func ReadDeclaration(path, unitName string) (Mode, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Mode{}, fmt.Errorf("读取模式声明失败: %w", err)
	}
	var d declaration
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true) // 未知字段即拒：声明是受审的表面，多出的字段是没人读过的意图
	if err := dec.Decode(&d); err != nil {
		if errors.Is(err, io.EOF) {
			return Mode{}, fmt.Errorf("模式声明 %s 是空文件：至少要写 id", filepath.Base(path))
		}
		return Mode{}, fmt.Errorf("解析模式声明 %s 失败: %w", filepath.Base(path), err)
	}
	id := canonicalKey(d.ID)
	if id == "" {
		return Mode{}, fmt.Errorf("模式声明 %s 缺少 id 字段", filepath.Base(path))
	}
	if want := canonicalKey(unitName); id != want {
		return Mode{}, fmt.Errorf("模式声明 %s 里的 id %q 与单元名 %q 不一致", filepath.Base(path), d.ID, unitName)
	}
	m, ok := byID[id]
	if !ok {
		return Mode{}, fmt.Errorf("内核不认识的对话模式 %q：新模式需要先在内核模式表里登记", d.ID)
	}
	if m.Builtin {
		return Mode{}, fmt.Errorf("内置模式 %q 不能被单元激活或覆盖", m.ID)
	}
	return m, nil
}

// Label 返回模式的展示文案；未知 id 原样返回（日志与旧数据里可能残留未登记的 id）。
func Label(id string) string {
	if m, ok := byID[canonicalKey(id)]; ok {
		return m.Label
	}
	return id
}
