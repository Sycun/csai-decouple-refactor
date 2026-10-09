const fs = require('node:fs');
const test = require('node:test');
const assert = require('node:assert/strict');

const html = fs.readFileSync('web/templates/index.html', 'utf8');
const settings = fs.readFileSync('web/static/js/settings.js', 'utf8');
const zh = JSON.parse(fs.readFileSync('web/static/i18n/zh-CN.json', 'utf8'));
const en = JSON.parse(fs.readFileSync('web/static/i18n/en-US.json', 'utf8'));

function quotaInput(id) {
    const m = html.match(new RegExp(`<input type="number" id="${id}"[^>]*>`));
    assert.ok(m, `index.html 缺少 ${id} 输入框`);
    return m[0];
}

function attr(tag, name) {
    const m = tag.match(new RegExp(`${name}="([^"]*)"`));
    return m ? m[1] : null;
}

test('额度设置输入框在 HTML 里就带默认值，JS 未跑也不留空框', () => {
    assert.equal(attr(quotaInput('openai-max-total-tokens'), 'value'), '120000');
    assert.equal(attr(quotaInput('openai-max-completion-tokens'), 'value'), '32768');
});

test('额度输入框占位符与提示和 i18n 默认值一致，不许漂回 16384', () => {
    const total = quotaInput('openai-max-total-tokens');
    const completion = quotaInput('openai-max-completion-tokens');
    assert.equal(attr(total, 'placeholder'), zh.settingsBasic.maxTotalTokensPlaceholder);
    assert.equal(attr(completion, 'placeholder'), zh.settingsBasic.maxCompletionTokensPlaceholder);
    assert.equal(attr(completion, 'placeholder'), en.settingsBasic.maxCompletionTokensPlaceholder);
    assert.ok(html.includes(`maxCompletionTokensHint">${zh.settingsBasic.maxCompletionTokensHint}</small>`));
    assert.ok(html.includes(`maxTotalTokensHint">${zh.settingsBasic.maxTotalTokensHint}</small>`));
});

test('默认值落在 min/step 网格上，浏览器不会把初值判成 stepMismatch', () => {
    for (const [id, fallback] of [['openai-max-total-tokens', '120000'], ['openai-max-completion-tokens', '32768']]) {
        const tag = quotaInput(id);
        const value = Number(attr(tag, 'value'));
        const min = Number(attr(tag, 'min'));
        const step = Number(attr(tag, 'step'));
        assert.equal(value, Number(fallback));
        assert.ok(value >= min, `${id} 初值低于 min`);
        assert.equal((value - min) % step, 0, `${id} 初值不在 step 网格上`);
    }
});

test('loadConfig 先写主表单再渲染通道下拉，渲染链路抛错也留住默认值', () => {
    const start = settings.indexOf('async function loadConfig(');
    assert.ok(start > -1, 'settings.js 缺少 loadConfig');
    const candidates = [
        settings.indexOf('\nfunction ', start + 1),
        settings.indexOf('\nasync function ', start + 1)
    ].filter((i) => i > -1);
    const body = settings.slice(start, Math.min(...candidates));
    const writeAt = body.indexOf('writeAIChannelToMainForm(selectedAIChannelId);');
    const renderAt = body.indexOf('renderAIChannelSelect();');
    assert.ok(writeAt > -1, 'loadConfig 里找不到写主表单调用');
    assert.ok(renderAt > -1, 'loadConfig 里找不到渲染通道下拉调用');
    assert.ok(writeAt < renderAt, '写主表单必须早于渲染通道下拉');
});
