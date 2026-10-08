const fs = require('node:fs');
const test = require('node:test');
const assert = require('node:assert/strict');

// 对话模式身份字面量（'eino_single' / 'plan_execute' / 'supervisor'）在 JS 与模板里的
// 出现账。唯一允许的活位置是 chat.js 的目录对象（window.csaiAgentModes）——它是前端消费
// /api/agent-modes 的唯一权威；其余是留账：monitor.js 的流渲染按 orchestration 分支、
// webshell.js 隐藏输入的初始占位。上限只降不升；新文件出现即红。
const ceilings = {
    'monitor.js': 10,
    'chat.js': 2,
    'webshell.js': 1,
};

test('对话模式身份字面量只在账上位置出现', () => {
    const dir = 'web/static/js';
    const files = fs.readdirSync(dir).filter(f => f.endsWith('.js'));
    const pattern = /['"](eino_single|plan_execute|supervisor)['"]/g;
    const over = [];
    let total = 0;
    for (const f of files) {
        const src = fs.readFileSync(dir + '/' + f, 'utf8');
        const n = (src.match(pattern) || []).length;
        total += n;
        const limit = ceilings[f] || 0;
        if (n > limit) over.push(`${f}: ${n} > ${limit}`);
    }
    assert.deepEqual(over, [],
        '模式清单与别名只允许从 window.csaiAgentModes 消费；要加字面量之前先问：能不能从目录取？');
    // 反空扫描地板：低于这个数说明匹配器坏了，而不是账变干净了。
    assert.ok(total >= 8, `只数到 ${total} 处——扫描器可能坏了`);
});

test('模板里的模式字面量只剩隐藏输入的初始占位', () => {
    const html = fs.readFileSync('web/templates/index.html', 'utf8');
    const pattern = /['"](eino_single|plan_execute|supervisor)['"]/g;
    const n = (html.match(pattern) || []).length;
    assert.ok(n <= 1, `index.html 出现 ${n} 处模式字面量；只有 agent-mode-select 隐藏输入的初始占位允许保留`);
});
