// /api/roles 每次刷新后要与「角色可能已被卸载」的事实对齐：选中项从服务端消失时退回默认，
// 否则界面显示默认、请求体却仍带着一个不存在的角色名。
// 实测路径：在能力包页卸载 web-pentest，而选中的正是它带来的角色。
const fs = require('node:fs');
const vm = require('node:vm');
const test = require('node:test');
const assert = require('node:assert/strict');

const source = fs.readFileSync('web/static/js/roles.js', 'utf8');

function harness(apiRoles, storedRole) {
    const store = { currentRole: storedRole || '' };
    const sandbox = {
        console,
        Promise,
        JSON,
        Set,
        Map,
        Date,
        localStorage: {
            getItem(k) { return k === 'currentRole' ? (store.currentRole || null) : null; },
            setItem(k, v) { if (k === 'currentRole') store.currentRole = String(v); },
        },
        document: {
            readyState: 'complete',
            addEventListener() {},
            getElementById() { return null; },
            createElement() {
                return {
                    className: '', style: {}, textContent: '',
                    setAttribute() {},
                    classList: { add() {}, remove() {}, toggle() {} },
                };
            },
            body: { appendChild() {} },
        },
        window: { addEventListener() {} },
        apiFetch() {
            return Promise.resolve({ ok: true, json: () => Promise.resolve({ roles: apiRoles }) });
        },
        showNotification() {},
    };
    sandbox.window.document = sandbox.document;
    vm.createContext(sandbox);
    vm.runInContext(source, sandbox);
    return { sandbox, store };
}

test('a selected role that disappeared from the server falls back to default', async () => {
    const { sandbox, store } = harness([{ name: '默认' }, { name: 'CTF' }], '渗透测试');
    const roles = await sandbox.loadRoles();

    assert.equal(store.currentRole, '', 'the vanished selection must be cleared, not kept');
    assert.deepEqual(roles.map(r => r.name), ['默认', 'CTF'], 'the fresh list itself must survive intact');
});

test('a selection that still exists is kept', async () => {
    const { sandbox, store } = harness([{ name: '默认' }, { name: 'CTF' }], 'CTF');
    await sandbox.loadRoles();

    assert.equal(store.currentRole, 'CTF');
});

test('a default selection is untouched by a refresh', async () => {
    const { sandbox, store } = harness([{ name: '默认' }], '');
    await sandbox.loadRoles();

    assert.equal(store.currentRole, '');
});

test('a failed load leaves the stored selection alone', async () => {
    const { sandbox, store } = harness([], '渗透测试');
    sandbox.apiFetch = () => Promise.resolve({ ok: false, status: 503, json: () => Promise.resolve({}) });

    await sandbox.loadRoles();
    assert.equal(store.currentRole, '渗透测试',
        'a failed refresh must not silently reset a selection the server never got to answer about');
});
