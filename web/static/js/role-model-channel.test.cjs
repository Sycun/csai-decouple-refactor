// 「一个角色一个模型」的前端契约：角色弹窗里的模型通道下拉、它读写的字段、以及它的词条。
//
// 后端那一半（ai_channel 的解析优先级）在 internal/handler/role_model_test.go；这里守的是
// 前端不会把绑定悄悄丢掉——尤其是「拉不到通道清单」和「绑定的通道已被删」两种情况，
// 静默清空会让下一次保存把 ai_channel 抹掉，而运维者看不出发生过什么。
const fs = require('node:fs');
const vm = require('node:vm');
const test = require('node:test');
const assert = require('node:assert/strict');

const source = fs.readFileSync('web/static/js/roles.js', 'utf8');
const template = fs.readFileSync('web/templates/index.html', 'utf8');

// roles.js builds a hover tooltip at load time (initSelectionDetailTooltip), so a created
// element has to look like an element and not just like an <option>.
function fakeEl(extra) {
    return Object.assign({
        value: '', textContent: '', innerHTML: '', className: '', style: {},
        children: [],
        appendChild(o) { this.children.push(o); },
        setAttribute() {}, getAttribute() { return null; }, removeAttribute() {},
        addEventListener() {}, removeEventListener() {},
        insertAdjacentHTML() {},
        classList: { add() {}, remove() {}, toggle() {}, contains() { return false; } },
    }, extra);
}

function harness({ channels = null, permission = true, bound = '' } = {}) {
    const select = fakeEl({ id: 'role-ai-channel', value: bound, innerHTML: 'keep-me' });
    const calls = [];
    const sandbox = {
        console, Promise, JSON, Set, Map, Date, Object, Array, String, Number,
        localStorage: { getItem() { return null; }, setItem() {}, removeItem() {} },
        document: {
            readyState: 'complete',
            addEventListener() {},
            getElementById(id) { return id === 'role-ai-channel' ? select : null; },
            createElement() { return fakeEl(); },
            body: { appendChild() {} },
        },
        window: { addEventListener() {} },
        hasPermission() { return permission; },
        apiFetch(url) {
            calls.push(url);
            if (channels === null) return Promise.resolve({ ok: false });
            return Promise.resolve({ ok: true, json: () => Promise.resolve({ ai: { channels } }) });
        },
        showNotification() {},
    };
    sandbox.window.document = sandbox.document;
    vm.createContext(sandbox);
    // escape.js and i18n-tag.js are loaded before roles.js in the template; running them here
    // keeps _t() on its real implementation instead of a stub that could drift from it.
    vm.runInContext(fs.readFileSync('web/static/js/escape.js', 'utf8'), sandbox);
    vm.runInContext(fs.readFileSync('web/static/js/i18n-tag.js', 'utf8'), sandbox);
    // In a browser window.CSAI *is* the global CSAI; in a vm context it is not, and roles.js
    // refers to the bare identifier.
    sandbox.CSAI = sandbox.window.CSAI;
    vm.runInContext(source, sandbox);
    // The real one rebuilds every enhanced select in the modal; none of them exist in this sandbox.
    sandbox.refreshRoleModalSelects = () => {};
    return { sandbox, select, calls };
}

const CHANNELS = {
    'qwen-max': { name: 'Qwen Max', model: 'qwen3-max' },
    deep: { name: 'Deep Think', model: 'model-deep' },
};

test('the channel dropdown offers "follow the default" plus every configured channel', async () => {
    const { sandbox, select } = harness({ channels: CHANNELS });
    await sandbox.loadAIChannelOptionsForRoleModal('');
    assert.deepEqual(select.children.map(o => o.value), ['', 'deep', 'qwen-max'],
        'an empty value must stay selectable: that is how "no binding" is expressed, and the backend reads it as ai.default_channel');
    assert.equal(select.value, '');
    assert.equal(select.children[1].textContent, 'Deep Think · model-deep',
        'the label follows the chat page convention (name · model) so one channel reads the same in both places');
    assert.equal(select.innerHTML, '', 'the previous option list must be cleared, not appended to');
});

test('a bound channel is selected when the modal opens for editing', async () => {
    const { sandbox, select } = harness({ channels: CHANNELS, bound: '' });
    await sandbox.loadAIChannelOptionsForRoleModal('deep');
    assert.equal(select.value, 'deep', 'editing a role must show the model it is bound to');
});

test('a binding whose channel is gone stays visible instead of being cleared', async () => {
    const { sandbox, select } = harness({ channels: CHANNELS });
    await sandbox.loadAIChannelOptionsForRoleModal('retired');
    assert.equal(select.value, 'retired',
        'dropping the value here would silently unbind the role on the next save');
    const kept = select.children.find(o => o.value === 'retired');
    assert.ok(kept && /retired/.test(kept.textContent), 'the stale id must be labelled, not shown as a plain option');
});

test('without config:read the dropdown is not populated from /api/config', async () => {
    const { sandbox, select, calls } = harness({ channels: CHANNELS, permission: false, bound: '' });
    await sandbox.loadAIChannelOptionsForRoleModal('deep');
    assert.deepEqual(calls, [], 'GET /api/config requires config:read; a roles-only operator must not be sent to it');
    assert.equal(select.children.length, 2, 'the empty option plus the retained binding, and nothing fetched');
    assert.equal(select.value, 'deep');
});

test('a failed config fetch degrades to the retained binding rather than throwing', async () => {
    const { sandbox, select, calls } = harness({ channels: null, bound: '' });
    await sandbox.loadAIChannelOptionsForRoleModal('deep');
    assert.deepEqual(calls, ['/api/config']);
    assert.equal(select.value, 'deep', 'an unreachable config must not cost the operator their binding');
});

test('the modal field, the select registry and the payload all agree on ai_channel', () => {
    assert.match(template, /<label for="role-ai-channel" data-i18n="roleModal\.aiChannel">/,
        'the label must be translated through data-i18n like every other field in this modal');
    assert.match(template, /<select id="role-ai-channel" class="role-form-select-native">/,
        'without role-form-select-native this dropdown renders as a bare native select next to two styled ones');
    assert.match(template, /<option value="" data-i18n="roleModal\.noAIChannelBind">/);

    assert.ok(source.includes("const ROLE_MODAL_SELECT_IDS = ['role-ai-channel', 'role-workflow-id', 'role-workflow-policy']"),
        'ROLE_MODAL_SELECT_IDS drives the enhanced-select wrapper; a missing id means an unstyled control');
    assert.match(source, /const aiChannel = aiChannelEl \? aiChannelEl\.value\.trim\(\) : '';/);
    assert.match(source, /ai_channel: aiChannel \|\| undefined/,
        'snake_case to match the Go json tag, and omitted when empty so the backend sees "no binding"');
    assert.match(source, /loadAIChannelOptionsForRoleModal\(role\.ai_channel \|\| ''\)/,
        'editRole must seed the dropdown from the stored binding');
    assert.match(source, /role\.ai_channel \?/, 'the role card shows which model a role is bound to');
});

test('the new wording exists in every shipped locale', () => {
    const keys = ['aiChannel', 'aiChannelHint', 'noAIChannelBind', 'aiChannelMissing'];
    for (const locale of ['zh-CN', 'en-US', 'ru-RU']) {
        const doc = JSON.parse(fs.readFileSync(`web/static/i18n/${locale}.json`, 'utf8'));
        for (const key of keys) {
            assert.ok(doc.roleModal && typeof doc.roleModal[key] === 'string' && doc.roleModal[key].trim() !== '',
                `roleModal.${key} missing from ${locale}`);
        }
    }
});
