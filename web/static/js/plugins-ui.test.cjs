const fs = require('node:fs');
const vm = require('node:vm');
const test = require('node:test');
const assert = require('node:assert/strict');

const source = fs.readFileSync('web/static/js/plugins.js', 'utf8');
const template = fs.readFileSync('web/templates/index.html', 'utf8');
const router = fs.readFileSync('web/static/js/router.js', 'utf8');
const zh = JSON.parse(fs.readFileSync('web/static/i18n/zh-CN.json', 'utf8'));
const en = JSON.parse(fs.readFileSync('web/static/i18n/en-US.json', 'utf8'));

function flatKeys(obj, prefix, out) {
    Object.keys(obj).forEach(k => {
        const v = obj[k];
        const path = prefix ? prefix + '.' + k : k;
        if (v && typeof v === 'object') flatKeys(v, path, out);
        else out.add(path);
    });
    return out;
}

test('the bundle console is reachable, and both locales carry the same keys', () => {
    assert.match(template, /<div class="nav-item" data-page="plugins-management">/);
    assert.match(template, /<div id="page-plugins-management" class="page">/);
    assert.match(template, /<script src="\/static\/js\/plugins\.js"><\/script>/);
    assert.match(template, /id="plugin-console"/);
    // Registered in the router twice on purpose: the hash whitelist on boot and the one used when
    // a page builds its own hash. A page missing from either is silently unreachable.
    const lists = router.split('\n').filter(l => l.includes("['dashboard'") && l.includes("'plugins-management'"));
    assert.equal(lists.length, 2, 'plugins-management must appear in both router page lists');
    assert.match(router, /case 'plugins-management':\s*\n\s*if \(typeof loadPluginConsole === 'function'\) loadPluginConsole\(\);/);

    const zhKeys = flatKeys(zh.plugins, '', new Set());
    const enKeys = flatKeys(en.plugins, '', new Set());
    const onlyZh = [...zhKeys].filter(k => !enKeys.has(k));
    const onlyEn = [...enKeys].filter(k => !zhKeys.has(k));
    assert.deepEqual(onlyZh, [], 'keys missing from en-US');
    assert.deepEqual(onlyEn, [], 'keys missing from zh-CN');
    assert.ok(zhKeys.size >= 35, 'the plugins namespace has gone thin: ' + zhKeys.size);
    assert.ok(zh.nav.plugins && en.nav.plugins, 'nav.plugins must exist in both locales');
});

function harness(state, catalog, options = {}) {
    const nodes = new Map();
    const calls = [];
    const toasts = [];
    function makeEl(id) {
        return {
            id,
            innerHTML: '',
            classList: { contains: () => true },
            insertAdjacentHTML: function (_pos, html) { this.innerHTML = html + this.innerHTML; },
        };
    }
    const sandbox = {
        document: {
            getElementById(id) {
                if (!nodes.has(id)) nodes.set(id, makeEl(id));
                return nodes.get(id);
            },
            createElement() {
                const el = { textContent: '', innerHTML: '' };
                Object.defineProperty(el, 'textContent', {
                    set(v) {
                        el._t = String(v == null ? '' : v);
                        // The browser's textContent -> innerHTML leaves quotes alone; the harness
                        // must not escape them, or the attribute-injection assertion tests nothing.
                        el.innerHTML = el._t.replace(/&/g, '&amp;').replace(/</g, '&lt;')
                            .replace(/>/g, '&gt;');
                    },
                    get() { return el._t; },
                });
                return el;
            },
        },
        console,
        Promise,
        JSON,
        Set,
        Date,
        escapeHtml(text) {
            const div = sandbox.document.createElement('div');
            div.textContent = text;
            return div.innerHTML;
        },
        escapeAttr(text) {
            return sandbox.escapeHtml(text).replace(/"/g, '&quot;').replace(/'/g, '&#39;');
        },
        showNotification(msg, type) { toasts.push({ msg, type }); },
        window: {
            // Interpolation is supported because the runtime-restart chip counts restarts in the
            // label; a harness that dropped opts would test a string no browser ever shows.
            t(key, opts) {
                const parts = key.split('.');
                let node = zh;
                for (const p of parts) {
                    if (!node || typeof node !== 'object') return key;
                    node = node[p];
                }
                let out = typeof node === 'string' ? node : key;
                if (opts && typeof out === 'string') {
                    Object.keys(opts).forEach(k => { out = out.replace('{' + k + '}', String(opts[k])); });
                }
                return out;
            },
            showNotification(msg, type) { toasts.push({ msg, type }); },
            confirm() { return options.confirm !== false; },
            applyRBACToUI() {},
        },
        apiFetch(url, opts = {}) {
            calls.push({ url, method: opts.method || 'GET', body: opts.body });
            // A test can pin a body onto one endpoint by substring, which is how the responses
            // that carry a caveat (switch_message, mcp_message) get exercised: the page has to
            // repeat what the server said, not just toast "done".
            const pinned = Object.keys(options.responses || {}).find(k => url.includes(k));
            const payload = pinned ? options.responses[pinned] : (url === '/api/plugins' ? state : catalog);
            if (options.fail && url === options.fail) {
                return Promise.resolve({
                    ok: false,
                    status: 409,
                    json: () => Promise.resolve({ error: options.error || '冲突' }),
                });
            }
            return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(payload) });
        },
    };
    sandbox.window.document = sandbox.document;
    vm.createContext(sandbox);
    vm.runInContext(source, sandbox);
    vm.runInContext(`
        this.api = { loadPluginConsole, installPluginBundle, unplugPluginBundle, setPluginUnitEnabled };
        this.pluginConsoleBusyReset = () => { pluginConsoleBusy = false; };
    `, sandbox);
    return { sandbox, nodes, calls, toasts };
}

const sampleState = {
    bundlesRoot: '/srv/csai/bundles',
    generation: 12,
    servedKinds: ['role', 'agent', 'skill', 'tool'],
    drift: [],
    bundles: [{
        id: 'mobile-app-security',
        name: '移动端安全测试角色包',
        version: '1.0.0',
        description: '示例包',
        dir: '/srv/csai/bundles/mobile-app-security',
        units: [
            { id: 'role/移动端安全测试', kind: 'role', name: '移动端安全测试', bundle: 'mobile-app-security', enabled: true, served: true, reason: '' },
            { id: 'mcp/示例', kind: 'mcp', name: '示例', bundle: 'mobile-app-security', enabled: false, served: false, reason: '外部 MCP 管理器已不再持有本包对该名称的声明（"lab-server" 由配置文件提供）' },
        ],
    }],
    standalone: [
        { id: 'role/evil"onclick="alert(1)', kind: 'role', name: 'evil"onclick="alert(1)', enabled: true, served: true, reason: '' },
    ],
};

const sampleCatalog = {
    bundlesRoot: '/srv/csai/bundles',
    bundles: [
        { id: 'mobile-app-security', name: '移动端安全测试角色包', version: '1.0.0', installed: true, units: [{ kind: 'role', name: '移动端安全测试' }] },
        { id: 'ai-app-redteam', name: 'AI 应用红队角色包', version: '1.0.0', installed: false, description: '按角色打包', units: [{ kind: 'role', name: 'AI应用红队测试' }, { kind: 'skill', name: 'llm-output-boundaries' }] },
        { id: 'broken-pack', name: 'broken-pack', version: '', installed: false, error: '解析配置文件失败', units: [] },
    ],
};

test('the console renders served state honestly and keeps the reason visible', async () => {
    const { sandbox } = harness(sampleState, sampleCatalog);
    await sandbox.api.loadPluginConsole();
    const html = sandbox.document.getElementById('plugin-console').innerHTML;

    assert.match(html, /plugin-chip-served/);
    assert.match(html, /未生效/);
    assert.match(html, /title="外部 MCP 管理器已不再持有本包对该名称的声明（&quot;lab-server&quot; 由配置文件提供）"/,
        'the reason must travel with the not-live mark, quoted so it cannot close the attribute');
    assert.match(html, /可安装的能力包/);
    assert.match(html, /AI 应用红队角色包/);
    assert.ok(!/onclick="installPluginBundle\(&quot;mobile-app-security/.test(html),
        'an installed pack must not be offered for install again');
    assert.match(html, /installPluginBundle\(&quot;ai-app-redteam&quot;\)/);
    assert.match(html, /清单不可用/);
    assert.ok(!/installPluginBundle\(&quot;broken-pack&quot;\)/.test(html),
        'a pack with an unreadable manifest must not offer an install button');
});

test('the emitted handlers are executed exactly as a browser would run them', async () => {
    const { sandbox, calls } = harness(sampleState, sampleCatalog);
    await sandbox.api.loadPluginConsole();
    const html = sandbox.document.getElementById('plugin-console').innerHTML;
    const decoded = h => h
        .replace(/&quot;/g, '"').replace(/&#39;/g, "'")
        .replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&amp;/g, '&');
    const attrs = (html.match(/onclick="[^"]*"/g) || []).map(decoded)
        .map(a => a.slice('onclick="'.length, -1));
    const switches = attrs.filter(a => a.startsWith('setPluginUnitEnabled('));
    assert.ok(switches.length >= 3, 'expected switch handlers to render, got ' + switches.length);
    assert.ok(!switches.some(a => /\(\[/.test(a)),
        'a switch handler was emitted with one array argument instead of three: ' + switches[0]);

    // Running the attribute text in the page's own context is the only way to catch an
    // argument-shape mismatch: an array handed to a three-parameter function still parses fine
    // when the attribute is merely inspected, and the server then sees
    // /units/tool,semgrep,false/undefined/enabled.
    const flush = async () => { for (let i = 0; i < 6; i++) await new Promise(r => setImmediate(r)); };
    const evil = switches.find(a => a.includes('alert(1)'));
    assert.ok(evil, 'the hostile unit name must still render a handler');
    sandbox.pluginConsoleBusyReset();
    vm.runInContext(evil, sandbox);
    await flush();
    const posts = calls.filter(c => c.method === 'POST' && c.url.endsWith('/enabled'));
    assert.equal(posts.length, 1,
        'exactly one POST then a reload: ' + JSON.stringify(calls.map(c => c.method + ' ' + c.url)));
    const last = posts[0];
    assert.match(last.url, /^\/api\/plugins\/units\/role\/[^/]+\/enabled$/, last.url);
    assert.equal(decodeURIComponent(last.url.split('/units/')[1].split('/enabled')[0].split('/')[1]),
        'evil"onclick="alert(1)');
    assert.deepEqual(JSON.parse(last.body), { enabled: false });
});

test('install, unplug and enable each hit one endpoint and reload the table', async () => {
    const { sandbox, calls, toasts } = harness(sampleState, sampleCatalog);
    await sandbox.api.installPluginBundle('ai-app-redteam');
    await sandbox.api.unplugPluginBundle('mobile-app-security');
    await sandbox.api.setPluginUnitEnabled('role', '移动端安全测试', false);

    const methods = calls.map(c => c.method + ' ' + c.url);
    assert.ok(methods.includes('POST /api/plugins/install'), methods.join(' | '));
    assert.ok(methods.includes('DELETE /api/plugins/bundles/mobile-app-security'), methods.join(' | '));
    assert.ok(methods.includes('POST /api/plugins/units/role/%E7%A7%BB%E5%8A%A8%E7%AB%AF%E5%AE%89%E5%85%A8%E6%B5%8B%E8%AF%95/enabled'), methods.join(' | '));
    const payload = JSON.parse(calls.find(c => c.url.endsWith('/enabled')).body);
    assert.deepEqual(payload, { enabled: false });
    const installBody = JSON.parse(calls.find(c => c.method === 'POST' && c.url === '/api/plugins/install').body);
    assert.deepEqual(installBody, { bundle: 'ai-app-redteam' });
    assert.equal(calls.filter(c => c.url === '/api/plugins').length, 3,
        'each mutation must re-read the table and the catalogue');
    assert.equal(calls.filter(c => c.url === '/api/plugins/available').length, 3);
    // A mutation that succeeded must not be reported as a failure: the feedback path is part of
    // the contract, and the real page once threw there (a toast helper that does not exist on
    // this page) and showed "install failed" over a 200 response.
    assert.deepEqual(toasts.map(t => t.type), ['success', 'success', 'success'], JSON.stringify(toasts));
    assert.match(toasts[0].msg, /能力包已安装/);
    assert.doesNotMatch(sandbox.document.getElementById('plugin-console').innerHTML, /安装失败|卸载失败|启停失败/);
});

test('a refused mutation is surfaced, not swallowed', async () => {
    const { sandbox, toasts } = harness(sampleState, sampleCatalog, { fail: '/api/plugins/install', error: 'role/移动端安全测试 已由 mobile-app-security 提供' });
    await sandbox.api.installPluginBundle('mobile-app-security');
    const html = sandbox.document.getElementById('plugin-console').innerHTML;
    assert.match(html, /安装失败/);
    assert.match(html, /已由 mobile-app-security 提供/);
    assert.equal(toasts.length, 0, 'a failed install must not toast success');
});

// Each of these is a promise the server made in the body. The page swallowing them is the failure
// mode this console has been rebuilt around: the request succeeded, the toast said 已更新, and the
// thing the operator clicked is not what is running.
test('the toast repeats what the server said about a mutation that only half took effect', async () => {
    const cases = [
        {
            name: 'switch that is not durable',
            key: 'units/mcp',
            url: '/api/plugins/units/mcp/%E7%A4%BA%E4%BE%8B/enabled',
            body: { switch_persisted: false, switch_message: '包声明的 MCP 服务器每次启动都回到停用状态，要跨重启常驻请写进 config.yaml' },
            run: api => api.setPluginUnitEnabled('mcp', '示例', true),
            expect: /config\.yaml/,
        },
        {
            name: 'install whose tool layer could not rebuild',
            key: '/api/plugins/install',
            url: '/api/plugins/install',
            body: { bundle: { id: 'mobile-app-security' }, tools_rebuilt: false, tool_layer_error: 'tool: 注册失败' },
            run: api => api.installPluginBundle('mobile-app-security'),
            expect: /注册失败/,
        },
        {
            name: 'declaration with no manager wired',
            key: 'bundles/mobile-app-security',
            url: '/api/plugins/bundles/mobile-app-security',
            body: { mcp_removed: 0, mcp_message: 'MCP 声明未接入外部 MCP 管理器' },
            run: api => api.unplugPluginBundle('mobile-app-security'),
            expect: /未接入/,
        },
    ];
    for (const tc of cases) {
        const { sandbox, toasts } = harness(sampleState, sampleCatalog, { responses: { [tc.key]: tc.body } });
        await tc.run(sandbox.api);
        assert.ok(toasts.length, tc.name + ': nothing was reported to the operator');
        const last = toasts[toasts.length - 1];
        assert.match(last.msg, tc.expect, tc.name + ': the caveat was dropped from the toast: ' + last.msg);
    }
});

test('a plugin row says what the live host holds, and no other kind does', async () => {
    // The pack console is where an operator learns their third-party binary is crash-looping; a
    // switch and a served flag are not enough, so the runtime row has to reach the table.
    const { sandbox } = harness({
        bundlesRoot: '/srv/csai/bundles', generation: 1, drift: [], servedKinds: ['plugin'],
        bundles: [], standalone: [],
        pluginHost: [{ domain: 'acme', bundle: 'code-pack', running: true, restarts: 2, grants: ['net.connect(10.0.0.0/8)'], fromPack: true }],
    }, {});
    // Loaded through the same entry the page uses, so the assertion covers GET /api/plugins ->
    // pluginConsoleState -> the row, not just the renderer.
    await sandbox.api.loadPluginConsole();

    const pluginRow = sandbox.unitCells({ kind: 'plugin', name: 'acme', served: true });
    assert.match(pluginRow, /进程运行中/, 'the running process must be visible on its unit row');
    assert.match(pluginRow, /重启 2 次/, 'restarts must be counted where a crash loop shows up');

    const stoppedRow = sandbox.unitCells({ kind: 'plugin', name: 'ghost', served: false });
    assert.match(stoppedRow, /未运行/, 'a plugin with no live instance says so instead of looking fine');

    // A non-plugin kind gets no runtime chip: it has no process of its own.
    const toolRow = sandbox.unitCells({ kind: 'tool', name: 'semgrep', served: true });
    assert.doesNotMatch(toolRow, /进程运行中|未运行/, 'only a plugin unit may claim a process state');
});

test('every pluginsT key the console asks for exists in both locales', () => {
    // A missing key renders as its own name - which is exactly how a new kind or a new runtime chip
    // would ship looking broken to every operator while all the Go tests stayed green.
    const used = [...source.matchAll(/pluginsT\('([A-Za-z0-9_.]+)'/g)].map(m => m[1]);
    assert.ok(used.length >= 30, `only ${used.length} pluginsT calls were found - the scan is broken`);
    const zhKeys = flatKeys(zh.plugins, '', new Set());
    const enKeys = flatKeys(en.plugins, '', new Set());
    // 'kind.' is the dynamic prefix (unitKindLabel builds kind.<name>), covered by
    // TestEveryCapabilityKindHasAConsoleLabel against plugin.Kinds.
    const statics = used.filter(k => !k.endsWith('.'));
    const missingZh = statics.filter(k => !zhKeys.has(k));
    const missingEn = statics.filter(k => !enKeys.has(k));
    assert.deepEqual(missingZh, [], 'keys missing from zh-CN');
    assert.deepEqual(missingEn, [], 'keys missing from en-US');
});
