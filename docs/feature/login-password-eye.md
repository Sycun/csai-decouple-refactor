
## F0 Intake

用户在确认内置眼睛按钮方案后要求“最佳实践优化”。仓库 main 干净，起点 762f798。独立分支 codex/login-password-eye。
Workflow Gate: P7 局部 UI 修整→P9 验证；上游为用户截图及已确认方案；无 API/权限变化，允许实现。
Maintainability Gate: auth.js 1014 行、模板 6987 行、style.css 46717 行。大文件风险高，但只替换现有组件的局部标记/样式/标签同步，不新增职责，按 narrow_fix 执行；无需整体重构。

## F1 Requirements

范围依据：用户明确要求执行上一轮方案。无需另行产品决策。
REQ-001 / AC-001: 密码框与用户名框同宽，右侧内置灰色线条眼睛，图标 20px、触控区域 44px。
REQ-002 / AC-002: 原生非提交按钮支持键盘 Enter/Space，焦点可见；显示/隐藏切换不改变密码值。
REQ-003 / AC-003: 图标随状态切换，显示密码/隐藏密码标签和悬浮提示随现有语言系统同步；登录框重新打开仍默认隐藏。
非目标：后端登录、密码策略及其他密码输入框。

## F2 Design

保留原按钮 ID 和 toggleLoginPasswordVisibility 入口；SVG 为装饰图像并 aria-hidden。按钮以 aria-label/title 命名，以现有 aria-pressed 表示状态；data-i18n-skip-text 防止翻译覆盖图标，data-i18n-attr 同步标签。
相对定位容器+绝对定位按钮；输入框预留右内边距，图标呈现由 aria-pressed 的 CSS 选择器控制。使用现有主题色、原生按钮交互，不引入依赖。

## F3 Plan

修改 index.html 的按钮内部结构，style.css 的局部布局和交互样式，auth.js 的标签同步，扩充已有 password-feedback.test.cjs 的状态/标签断言。
验证：已有密码反馈测试、JS syntax、diff check，实际浏览器桌面/窄屏与语言、键盘切换检查。回退可 revert 本功能代码提交。发布记录在 CHANGELOG.md Unreleased；不合并或发布。

## F4 Implementation

已替换独立文字按钮为输入框内 20px SVG 眼睛，44px 原生按钮点击区域，登录两输入框最小高度 48px；支持主题色及 focus-visible。JS 不再替换按钮 textContent，使用 aria-label/title 同步现有翻译键；模板标记支持语言重绘。更新 CSS/auth.js 缓存版本。已有密码反馈测试 4/4、JS syntax 和 diff check 通过。

## F5 Verification

- node --test web/static/js/password-feedback.test.cjs: 4/4 PASS。
- node --check web/static/js/auth.js、git diff --check: PASS。
- Headless Chrome / Playwright 使用实际登录模板、style.css/mobile.css、实际显隐函数和 applyTranslations；1280/390/320px 各检查等宽输入框、44×44 点击区、Space/Enter 不提交表单的显隐、值不变、focus-visible、双图标切换和中英俄文 aria-label/title 同步，全部 PASS。
- 六张浅色/深色截图已生成，人工查看 1280px 浅色和 320px 深色；布局正常。验证完成登录入场动画后进行尺寸测量。
- 未进行真实服务登录和后端测试：本次未改变认证请求或后端。未执行整个 JS 套件。
- F0–F3 文档提交 2f2e01e/30b680d/6a31e29/4d397a3；F4 代码提交 d590cdd，均已推送。

## F6 Review / Release Record

局部复核完成：CSS 选择器可覆盖共享输入框规则，按钮为 type=button；翻译只更新属性不会移除 SVG；沿用 aria-controls/aria-pressed；auth/CSS URL 版本同时更新避免旧缓存。CHANGELOG.md 已登记 Unreleased 修复。
全部完成阶段均保留文档并提交推送至 codex/login-password-eye。未创建 PR、未合并 main、未发布版本。合并前可使用以下说明：

> 登录页“显示密码”按钮占用输入框宽度并抢占视觉重点。改为内置眼睛按钮，统一两输入框宽高，并保留显隐、键盘和多语言无障碍行为。验证：4 项密码反馈测试、JS syntax/diff check、桌面和两种窄屏 Chrome 检查（浅/深主题、中英俄语言、键盘及点击交互）。

## F8 Merge Summary — 2026-10-08

用户授权合并 main。合并前重新 fetch，origin/main 仍为 762f798，4 项密码反馈测试、JS syntax、diff check 再次通过。功能分支以 fast-forward 合并，本地和远端 main 已推送并核对为 4f64347752860add4c0beb2e03a0ae08706e1cf7。核心实现提交 d590cdd；未创建 PR，未发布版本或部署运行服务。本节为合并后的追踪记录，替代 F6 的未合并状态。
