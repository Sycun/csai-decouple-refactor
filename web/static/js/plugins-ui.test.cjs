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
    // 进入对话页时角色列表要对齐服务端：能力可以在能力包页被安装/卸载，而对话页的角色侧栏
    // 只在浏览器加载时取过一次——这正是「装完要刷新浏览器」的根因。
    const chatCase = router.split("case 'chat':")[1].split('break;')[0];
    assert.match(chatCase, /typeof loadRoles === 'function'[\s\S]*?loadRoles\(\);/,
        'entering chat must re-read the role list instead of keeping the load-time copy');
    assert.match(chatCase, /csaiAgentModes\.refresh\(\);/,
        'entering chat must re-read the agent-mode catalog as well: an orchestration pack can be (un)installed from another tab or the robot side');

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
    const confirms = [];
    function makeEl(id) {
        return {
            id,
            innerHTML: '',
            // contains() stays true on purpose: the failure path draws its banner only when
            // isPluginConsoleActive() answers true, and that path is under test here. The mutators
            // are no-ops so handlers that flip classes still run end to end.
            classList: { contains: () => true, add() {}, remove() {}, toggle() {} },
            setAttribute() {},
            insertAdjacentHTML: function (_pos, html) { this.innerHTML = html + this.innerHTML; },
            // The focus/selection surface the filter-bar rebuild carries across: the harness
            // cannot destroy elements the way innerHTML does in a browser, but it records the
            // calls, which is the contract the rebuild must honour.
            selectionStart: 0,
            selectionEnd: 0,
            focusCalls: 0,
            selectionRestored: null,
            focus() { this.focusCalls++; },
            setSelectionRange(a, b) { this.selectionRestored = [a, b]; },
        };
    }
    const sandbox = {
        document: {
            activeElement: null,
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
        Map,
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
            // Deliberately no interpolation: the real page's i18next returns the resource string
            // verbatim when a {placeholder} is passed, and a harness that substituted one would
            // have kept asserting "重启 2 次" while the browser showed "重启 {count} 次".
            t(key) {
                const parts = key.split('.');
                let node = zh;
                for (const p of parts) {
                    if (!node || typeof node !== 'object') return key;
                    node = node[p];
                }
                return typeof node === 'string' ? node : key;
            },
            showNotification(msg, type) { toasts.push({ msg, type }); },
            confirm(msg) { confirms.push(msg); return options.confirm !== false; },
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
        this.api = { loadPluginConsole, installPluginBundle, unplugPluginBundle, setPluginUnitEnabled,
                     rollbackPluginBundle, applyPluginFilter, switchPluginTab, togglePluginUnitChoice,
                     selectAllPluginUnits, applyPluginKindFilter,
                     chosenFor: packID => {
                         const pack = (pluginConsoleCatalog.bundles || []).filter(b => b.id === packID)[0];
                         const installed = (pluginConsoleState.bundles || []).filter(b => b.id === packID)[0];
                         return chosenUnitIDs(pack, installed);
                     } };
        this.pluginConsoleBusyReset = () => { pluginConsoleBusy = false; };
        this.pluginTabState = () => pluginConsoleTab;
    `, sandbox);
    return { sandbox, nodes, calls, toasts, confirms };
}

const sampleState = {
    bundlesRoot: '/srv/csai/bundles',
    generation: 12,
    servedKinds: ['role', 'agent', 'skill', 'tool', 'plugin'],
    drift: [],
    revocations: {
        loaded: true,
        source: '/etc/csai/revocations.json',
        digests: ['deadbeef'],
        publishers: ['bad-pub'],
    },
    pluginHost: [],
    bundles: [{
        id: 'mobile-app-security',
        name: '移动端安全测试角色包',
        version: '1.0.0',
        description: '示例包',
        rollbacks: ['0.9.0', '1.0.0'],
        units: [
            { id: 'role/移动端安全测试', kind: 'role', name: '移动端安全测试', bundle: 'mobile-app-security', enabled: true, served: true, reason: '', digest: '1111222233334444' },
            { id: 'mcp/示例', kind: 'mcp', name: '示例', bundle: 'mobile-app-security', enabled: false, served: false, reason: '外部 MCP 管理器已不再持有本包对该名称的声明（"lab-server" 由配置文件提供）', digest: '5555666677778888' },
            { id: 'plugin/acme', kind: 'plugin', name: 'acme', bundle: 'mobile-app-security', enabled: false, served: false, reason: '已声明、未启用：插件二进制只在能力包里，运行它是运维者的决定', digest: 'aaaa000011112222', publisher: 'acme', artifactDigest: 'abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789', revoked: true },
        ],
    }, {
        id: 'reporting-pack',
        name: '报告角色包',
        version: '1.0.0',
        description: 'v1',
        rollbacks: ['0.9.0', '1.0.0'],
        units: [
            { id: 'role/报告撰写', kind: 'role', name: '报告撰写', bundle: 'reporting-pack', enabled: true, served: true, reason: '', digest: 'oldoldoldold' },
            { id: 'tool/pandoc', kind: 'tool', name: 'pandoc', bundle: 'reporting-pack', enabled: true, served: true, reason: '', digest: 'tooltooltool' },
        ],
    }],
    standalone: [
        { id: 'role/evil"onclick="alert(1)', kind: 'role', name: 'evil"onclick="alert(1)', enabled: true, served: true, reason: '' },
    ],
};

const sampleCatalog = {
    bundlesRoot: '/srv/csai/bundles',
    bundles: [
        {
            id: 'mobile-app-security', name: '移动端安全测试角色包', version: '1.0.0', installed: true,
            units: [
                { id: 'role/移动端安全测试', kind: 'role', name: '移动端安全测试', installed: true, digest: '1111222233334444' },
                { id: 'mcp/示例', kind: 'mcp', name: '示例', installed: false, digest: '5555666677778888' },
                { id: 'plugin/acme', kind: 'plugin', name: 'acme', installed: false, digest: 'aaaa000011112222', publisher: 'acme' },
            ],
        },
        {
            id: 'ai-app-redteam',
            name: 'AI 应用红队角色包',
            version: '1.0.0',
            installed: false,
            description: '按角色打包',
            author: 'acme',
            homepage: 'https://example.invalid/ai',
            license: 'Apache-2.0',
            compatibility: '>=0.9',
            categories: ['红队', 'AI'],
            preview: { classes: { readonly: 1, destructive: 1 }, liveCodeUnits: 1, undeclaredUnits: 1, problemUnits: 0, units: [] },
            units: [
                { id: 'role/AI应用红队测试', kind: 'role', name: 'AI应用红队测试', installed: false, digest: 'aaa' },
                { id: 'skill/llm-output-boundaries', kind: 'skill', name: 'llm-output-boundaries', installed: false, digest: 'bbb' },
                { id: 'tool/loose', kind: 'tool', name: 'loose', installed: false, digest: 'ccc' },
            ],
        },
        {
            id: 'reporting-pack',
            name: '报告角色包',
            version: '2.0.0',
            installed: true,
            description: 'v2 目录',
            categories: ['报告'],
            preview: { classes: { mutating: 1 }, liveCodeUnits: 0, undeclaredUnits: 0, problemUnits: 0, units: [] },
            units: [
                { id: 'role/报告撰写', kind: 'role', name: '报告撰写', installed: true, digest: 'newnewnewnew' },
                { id: 'tool/pandoc', kind: 'tool', name: 'pandoc', installed: true, digest: 'tooltooltool' },
                { id: 'agent/report-analyst', kind: 'agent', name: 'report-analyst', installed: true, digest: 'agentagentagent' },
            ],
        },
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
    assert.match(html, /能力货架/);
    assert.match(html, /AI 应用红队角色包/);
    assert.match(html, /onclick="installPluginBundle\(&quot;mobile-app-security&quot;\)"/,
        'an installed pack stays on the shelf: that card is where its selection is adjusted');
    assert.match(html, /更新选择 \(1\/3\)/,
        'the installed card must state the current selection and offer to change it');
    assert.match(html, /未装/, 'units that are not installed must be marked as such');
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
            name: 'install that could not be recorded',
            key: '/api/plugins/install',
            url: '/api/plugins/install',
            body: { bundle: { id: 'ai-app-redteam' }, install_recorded: false, install_message: '安装记录存储不可用：本次安装只在本次进程内有效' },
            run: api => api.installPluginBundle('ai-app-redteam'),
            expect: /安装记录存储不可用/,
        },
        {
            name: 'install whose rollback snapshot could not be written',
            key: '/api/plugins/install',
            url: '/api/plugins/install',
            body: { bundle: { id: 'ai-app-redteam' }, snapshot_version: '', snapshot_error: '创建快照目录失败' },
            run: api => api.installPluginBundle('ai-app-redteam'),
            expect: /创建快照目录失败/,
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

// P0-1: the click is preceded by what the pack would register. The counts come from the
// server-read preview, and cancelling the dialog must not touch the endpoint.
test('install asks first, in terms of classes, live code and undeclared recipes', async () => {
    const decided = harness(sampleState, sampleCatalog);
    // The real flow always has the catalogue loaded before a button exists; load it here so the
    // confirm text is built from the preview the way the page builds it.
    await decided.sandbox.api.loadPluginConsole();
    await decided.sandbox.api.installPluginBundle('ai-app-redteam');
    assert.equal(decided.confirms.length, 1, 'install must go through a confirm dialog');
    const text = decided.confirms[0];
    assert.match(text, /确认安装能力包「AI 应用红队角色包」/, text);
    assert.match(text, /声明能力：破坏性 × 1 · 只读 × 1/, text);
    assert.match(text, /包含可执行代码或进程/, text);
    assert.match(text, /无声明的工具配方 1 个/, text);
    assert.equal(decided.calls.filter(c => c.method === 'POST').length, 1);

    const cancelled = harness(sampleState, sampleCatalog, { confirm: false });
    await cancelled.sandbox.api.loadPluginConsole();
    await cancelled.sandbox.api.installPluginBundle('ai-app-redteam');
    const posts = cancelled.calls.filter(c => c.method === 'POST');
    assert.equal(posts.length, 0, 'a cancelled install must not reach the endpoint');
});

// P0-2: a directory version that moved forward is an upgrade, said out loud and diffed by unit
// before it is accepted; the install record and the toast name it as an upgrade, not a fresh
// install.
test('an installed pack with a newer directory version offers the upgrade, with its diff', async () => {
    const { sandbox, calls, toasts, confirms } = harness(sampleState, sampleCatalog);
    await sandbox.api.loadPluginConsole();
    const html = sandbox.document.getElementById('plugin-console').innerHTML;
    assert.match(html, /已装 v1\.0\.0/, 'the card must name the installed version next to the on-disk one');
    assert.match(html, /目录已是 v2\.0\.0（可升级）/);
    assert.match(html, /单元变化：新增 1 个 · 变更 1 个/, 'the diff must count added and changed units by digest');
    assert.match(html, /onclick="installPluginBundle\(&quot;reporting-pack&quot;\)"/, 'the upgrade button must emit the same endpoint');

    await sandbox.api.installPluginBundle('reporting-pack');
    assert.match(confirms[0], /确认升级能力包「报告角色包」/);
    assert.match(confirms[0], /将升级：v1\.0\.0 → v2\.0\.0/);
    const body = JSON.parse(calls.find(c => c.method === 'POST' && c.url === '/api/plugins/install').body);
    assert.deepEqual(body, { bundle: 'reporting-pack' }, 'an upgrade is a re-install; no from_version is implied');
    assert.match(toasts[toasts.length - 1].msg, /能力包已升级/);
});

// P0-2: rollback buttons are the snapshots that actually exist (the installed version itself is
// not a target), and the click names both the pack and the version it will restore.
test('rollback offers real snapshot versions and posts the version it will restore', async () => {
    const { sandbox, calls, toasts, confirms } = harness(sampleState, sampleCatalog);
    await sandbox.api.loadPluginConsole();
    const html = sandbox.document.getElementById('plugin-console').innerHTML;
    assert.match(html, /可回滚版本/);
    assert.match(html, /onclick="rollbackPluginBundle\(&quot;reporting-pack&quot;,&quot;0\.9\.0&quot;\)"/);
    assert.ok(!/rollbackPluginBundle\(&quot;reporting-pack&quot;,&quot;1\.0\.0&quot;\)/.test(html),
        'the installed version must not be offered as a rollback target');

    await sandbox.api.rollbackPluginBundle('reporting-pack', '0.9.0');
    assert.match(confirms[0], /确认把能力包「reporting-pack」回滚到 v0\.9\.0/, confirms[0]);
    const body = JSON.parse(calls.find(c => c.method === 'POST' && c.url === '/api/plugins/install').body);
    assert.deepEqual(body, { bundle: 'reporting-pack', from_version: '0.9.0' });
    assert.match(toasts[toasts.length - 1].msg, /能力包已回滚/);

    const cancelled = harness(sampleState, sampleCatalog, { confirm: false });
    await cancelled.sandbox.api.rollbackPluginBundle('reporting-pack', '0.9.0');
    assert.equal(cancelled.calls.length, 0, 'a cancelled rollback must not reach the endpoint');
});

// P0-3: provenance, digest and the revocation list are read off the state and rendered where an
// operator can act on them, instead of being invisible facts about what is running.
test('rows carry digest, publisher and revoked state, and the revocation panel names its source', async () => {
    const { sandbox } = harness(sampleState, sampleCatalog);
    await sandbox.api.loadPluginConsole();
    const html = sandbox.document.getElementById('plugin-console').innerHTML;

    assert.match(html, /发布者 acme/);
    assert.match(html, /已撤销/);
    assert.match(html, /aaaa00001111/, 'the digest chip must show a short form of the install-time digest');
    assert.match(html, /title="命中撤销列表（按发布者或构建摘要）：调用会被执行路径拒绝"/);
    assert.match(html, /信任与撤销/);
    assert.match(html, /\/etc\/csai\/revocations\.json/);
    assert.match(html, /bad-pub/);
    assert.match(html, /按构建撤销/);
});

// P0-4: the catalogue metadata renders, and the filter narrows both lists without losing the
// query.
// 逐键输入不能把焦点打飞：筛选条每次键入都会重渲染（chips 要跟随查询词回显），输入框又
// 住在筛选条里——重建必须把焦点和光标带回输入框，否则敲一个字符焦点就丢一次。
test('the text filter keeps focus and cursor across re-renders', async () => {
    const { sandbox, nodes } = harness(sampleState, sampleCatalog);
    await sandbox.api.loadPluginConsole();
    const input = sandbox.document.getElementById('plugin-filter-input');
    input.selectionStart = 2;
    input.selectionEnd = 2;
    sandbox.document.activeElement = input;
    sandbox.api.applyPluginFilter('角色');
    assert.equal(input.focusCalls, 1, 'the rebuilt input was never refocused - a keystroke costs the focus');
    assert.deepEqual(input.selectionRestored, [2, 2], 'the cursor was not carried across the rebuild');
});

test('catalogue metadata renders and the filter narrows the lists', async () => {    const { sandbox } = harness(sampleState, sampleCatalog);
    await sandbox.api.loadPluginConsole();
    let html = sandbox.document.getElementById('plugin-console').innerHTML;
    assert.match(html, /作者 acme/);
    assert.match(html, /许可 Apache-2\.0/);
    assert.match(html, /兼容 &gt;=0\.9/);
    assert.match(html, /主页 https:\/\/example\.invalid\/ai/);
    assert.match(html, /placeholder="搜索能力包（名称、描述、分类）"/);

    sandbox.api.applyPluginFilter('ai-app');
    const avail = sandbox.document.getElementById('plugin-section-available').innerHTML;
    const inst = sandbox.document.getElementById('plugin-section-installed').innerHTML;
    assert.match(avail, /ai-app-redteam/);
    assert.ok(!/reporting-pack/.test(avail), 'a non-matching pack must leave the filtered list');
    assert.ok(!/mobile-app-security/.test(inst));
    assert.match(inst, /没有匹配的能力包/);

    sandbox.api.applyPluginFilter('');
    const restored = sandbox.document.getElementById('plugin-section-installed').innerHTML;
    assert.match(restored, /mobile-app-security/);
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

// 能力在能力包页装好之后，对话页的角色侧栏仍停在上次整页加载时的那份 /api/roles 上——
// 这就是实测到的「装完要刷新浏览器才看得见」。变更成功必须把别的页在内存里各留一份的
// 清单一起重读；拒绝的变更什么都没改，不许触发重读。
test('a successful mutation re-reads the role lists the other pages keep in memory', async () => {
    const { sandbox } = harness(sampleState, sampleCatalog);
    await sandbox.api.loadPluginConsole();
    let roleLoads = 0;
    let wsLoads = 0;
    sandbox.loadRoles = () => { roleLoads++; return Promise.resolve([]); };
    sandbox.wsLoadRoles = () => { wsLoads++; };
    delete sandbox.window._mentionToolsRoleChanged;

    await sandbox.api.installPluginBundle('ai-app-redteam');
    assert.equal(roleLoads, 1, 'the chat role sidebar must be re-read right after the install');
    assert.equal(wsLoads, 1, 'the webshell role copy must be re-read right after the install');
    assert.equal(sandbox.window._mentionToolsRoleChanged, true,
        'the @ tool list must be invalidated: a pack can add tools without the role changing');

    await sandbox.api.unplugPluginBundle('mobile-app-security');
    await sandbox.api.setPluginUnitEnabled('role', '移动端安全测试', false);
    assert.equal(roleLoads, 3, 'every successful mutation re-reads the shared role list');
    assert.equal(wsLoads, 3);
});

test('a refused mutation re-reads nothing', async () => {
    const { sandbox } = harness(sampleState, sampleCatalog, { fail: '/api/plugins/install', error: '冲突' });
    await sandbox.api.loadPluginConsole();
    let roleLoads = 0;
    sandbox.loadRoles = () => { roleLoads++; return Promise.resolve([]); };
    await sandbox.api.installPluginBundle('mobile-app-security');

    assert.equal(roleLoads, 0, 'nothing changed on the server, so the role list must not be re-read');
});

// 安装 toast 要说出装了什么：数量来自响应里的 bundle.units（服务端读盘后的答复），
// 而不是点击参数——这才让「装完就看见拿到了什么」与其它字段同一判据。
test('the install toast names what the pack brought, counted from the response', async () => {
    const { sandbox, toasts } = harness(sampleState, sampleCatalog, {
        responses: {
            '/api/plugins/install': {
                bundle: {
                    id: 'reporting-pack',
                    units: [
                        { kind: 'role', name: '报告撰写' },
                        { kind: 'role', name: '报告审核' },
                        { kind: 'skill', name: 'report-format' },
                    ],
                },
            },
        },
    });
    await sandbox.api.loadPluginConsole();
    await sandbox.api.installPluginBundle('reporting-pack');
    assert.match(toasts[toasts.length - 1].msg, /角色 × 2、技能 × 1/,
        'the toast must count the installed units: ' + toasts[toasts.length - 1].msg);
});

// 「可安装 / 已安装」是标签页而不是一直摊开的两段：点哪个显示哪个（交互对齐系统设置），
// 默认停在可安装。一次变更会整页重渲染，所以重渲染后必须落回用户点开的那一页——
// 否则装完一个包等于把用户甩回第一页，装到哪儿去了又要翻。
test('the two lists are tabs: only the open one shows, and it survives re-renders', async () => {
    const { sandbox } = harness(sampleState, sampleCatalog);
    await sandbox.api.loadPluginConsole();
    const consoleHtml = () => sandbox.document.getElementById('plugin-console').innerHTML;

    const before = consoleHtml();
    assert.match(before, /class="dashboard-feed-tab is-active" role="tab" id="plugin-tab-available"/,
        'the available tab must be the default');
    assert.match(before, /id="plugin-section-available" role="tabpanel" aria-labelledby="plugin-tab-available">/);
    assert.match(before, /id="plugin-section-installed" role="tabpanel" aria-labelledby="plugin-tab-installed" hidden>/,
        'the installed list must start hidden');
    assert.match(before, /能力货架/);
    assert.match(before, /已安装的能力包/);

    // 跑一遍真实 onclick 文本，而不是直接调函数：参数形状错了照样在这里现形。
    const decoded = s => s.replace(/&quot;/g, '"').replace(/&#39;/g, "'");
    const tabClicks = (before.match(/onclick="switchPluginTab\([^"]*\)"/g) || [])
        .map(a => decoded(a.slice('onclick="'.length, -1)));
    assert.equal(tabClicks.length, 2, tabClicks.join(' | '));
    vm.runInContext(tabClicks[1], sandbox);
    assert.equal(sandbox.pluginTabState(), 'installed', 'the emitted handler must switch the tab');

    sandbox.window.renderPluginConsole();
    const after = consoleHtml();
    assert.match(after, /class="dashboard-feed-tab is-active" role="tab" id="plugin-tab-installed"/);
    assert.match(after, /id="plugin-section-installed" role="tabpanel" aria-labelledby="plugin-tab-installed">/);
    assert.match(after, /id="plugin-section-available" role="tabpanel" aria-labelledby="plugin-tab-available" hidden>/);

    // 变更后的重渲染（安装会重读表格）也要停在这一页
    await sandbox.api.installPluginBundle('ai-app-redteam');
    const afterInstall = consoleHtml();
    assert.match(afterInstall, /class="dashboard-feed-tab is-active" role="tab" id="plugin-tab-installed"/,
        'a mutation re-render must land back on the tab the operator had open');
});

// 按单元挑选：货架卡就是一张勾选表，勾了什么就发什么，"更新选择"是同一张卡上的重发。
test('the shelf card is a checklist: the ticks decide what the install sends', async () => {
    const { sandbox, calls, confirms } = harness(sampleState, sampleCatalog);
    await sandbox.api.loadPluginConsole();

    // 已装包默认按"已装"勾：mcp 与 plugin 两个单元没装过，因此不勾，也不会被一次点击顺手装进来。
    assert.equal(sandbox.api.chosenFor('mobile-app-security').length, 1,
        'an installed pack must default to what is installed, not to the whole manifest');

    // 补装：这次只要 MCP 那个单元——勾上它、取消"已装的那个"，选择就只剩它一个。
    sandbox.api.togglePluginUnitChoice('mobile-app-security', 'mcp/示例', true);
    assert.deepEqual(Array.from(sandbox.api.chosenFor('mobile-app-security')), ['role/移动端安全测试', 'mcp/示例']);
    sandbox.api.togglePluginUnitChoice('mobile-app-security', 'role/移动端安全测试', false);
    assert.deepEqual(Array.from(sandbox.api.chosenFor('mobile-app-security')), ['mcp/示例']);

    await sandbox.api.installPluginBundle('mobile-app-security');
    const body = JSON.parse(calls.find(c => c.method === 'POST' && c.url === '/api/plugins/install').body);
    assert.deepEqual(body, { bundle: 'mobile-app-security', units: ['mcp/示例'] },
        'the ticked units must travel with the request');
    assert.match(confirms[0], /本次选择 1\/3 个单元/);
    assert.match(confirms[0], /将移除 role\/移动端安全测试/,
        'the dialog must name the unit that would leave, not just the ones arriving');
});

// 全不选不是"装个空的"：按钮禁用，兜底调用也被挡在请求之前。
test('an empty selection cannot be installed', async () => {
    const { sandbox, calls, toasts } = harness(sampleState, sampleCatalog);
    await sandbox.api.loadPluginConsole();
    sandbox.api.selectAllPluginUnits('ai-app-redteam', false);
    assert.deepEqual(Array.from(sandbox.api.chosenFor('ai-app-redteam')), []);
    await sandbox.api.installPluginBundle('ai-app-redteam');
    assert.equal(calls.filter(c => c.method === 'POST' && c.url === '/api/plugins/install').length, 0,
        'nothing may be sent for an empty selection');
    assert.equal(toasts[toasts.length - 1].type, 'error');
});

// 全选回到整包形状：勾满且无冲突项时不带 units，服务端也按"跟随目录"记录。
test('selecting every unit sends the whole-pack shape', async () => {
    const { sandbox, calls } = harness(sampleState, sampleCatalog);
    await sandbox.api.loadPluginConsole();
    sandbox.api.selectAllPluginUnits('ai-app-redteam', true);
    await sandbox.api.installPluginBundle('ai-app-redteam');
    const body = JSON.parse(calls.find(c => c.method === 'POST' && c.url === '/api/plugins/install').body);
    assert.deepEqual(body, { bundle: 'ai-app-redteam' });
});

// 整包装的包升级时，新版本带来的单元默认勾上：运维者当初要的是"这个包"而不是一张冻结的
// 清单。默认不勾会让这次升级发出显式 units，安装记录就从"跟随目录"静默降级成清单，此后
// 新版再加单元也不再自动带入——而且没有任何提示。
test('an upgrade of a whole-pack install defaults arriving units to ticked', async () => {
    const state = Object.assign({}, sampleState, {
        bundles: [{
            id: 'growing-pack', name: '成长包', version: '1.0.0', unitsInstalled: 2, unitsTotal: 2,
            units: [
                { id: 'role/a', kind: 'role', name: 'a', bundle: 'growing-pack', installed: true, enabled: true, served: true },
                { id: 'role/b', kind: 'role', name: 'b', bundle: 'growing-pack', installed: true, enabled: true, served: true },
            ],
        }],
    });
    const catalog = {
        bundlesRoot: '/srv/csai/bundles',
        bundles: [{
            id: 'growing-pack', name: '成长包', version: '2.0.0', installed: true,
            units: [
                { id: 'role/a', kind: 'role', name: 'a', installed: true },
                { id: 'role/b', kind: 'role', name: 'b', installed: true },
                { id: 'role/c', kind: 'role', name: 'c', installed: false },
            ],
        }],
    };
    const { sandbox, calls } = harness(state, catalog);
    await sandbox.api.loadPluginConsole();
    assert.deepEqual(Array.from(sandbox.api.chosenFor('growing-pack')), ['role/a', 'role/b', 'role/c'],
        'a whole-pack install follows the pack: a unit arriving with the upgrade belongs to it');
    await sandbox.api.installPluginBundle('growing-pack');
    const body = JSON.parse(calls.find(c => c.method === 'POST' && c.url === '/api/plugins/install').body);
    assert.deepEqual(body, { bundle: 'growing-pack' },
        'ticked-everything is the whole-pack shape, so the record stays "follow the directory"');
});

// 反向同样钉死：部分安装的包新增单元默认不勾——运维者挑过的清单不能被一次升级悄悄扩大。
test('an upgrade of a partial install leaves arriving units unticked', async () => {
    const state = Object.assign({}, sampleState, {
        bundles: [{
            id: 'growing-pack', name: '成长包', version: '1.0.0', unitsInstalled: 1, unitsTotal: 2,
            units: [
                { id: 'role/a', kind: 'role', name: 'a', bundle: 'growing-pack', installed: true, enabled: true, served: true },
                { id: 'role/b', kind: 'role', name: 'b', bundle: 'growing-pack', installed: false, enabled: false, served: false },
            ],
        }],
    });
    const catalog = {
        bundlesRoot: '/srv/csai/bundles',
        bundles: [{
            id: 'growing-pack', name: '成长包', version: '2.0.0', installed: true,
            units: [
                { id: 'role/a', kind: 'role', name: 'a', installed: true },
                { id: 'role/b', kind: 'role', name: 'b', installed: false },
                { id: 'role/c', kind: 'role', name: 'c', installed: false },
            ],
        }],
    };
    const { sandbox } = harness(state, catalog);
    await sandbox.api.loadPluginConsole();
    assert.deepEqual(Array.from(sandbox.api.chosenFor('growing-pack')), ['role/a'],
        'a partial install keeps its honest default: what was never chosen stays unticked');
});

// 已装卡上的"摘除"走单元端点，并跑一遍真实 onclick 文本（形状错了在这里现形）。
test('removing one unit from an installed pack runs the emitted handler', async () => {
    const { sandbox, calls, toasts } = harness(sampleState, sampleCatalog);
    await sandbox.api.loadPluginConsole();
    const html = sandbox.document.getElementById('plugin-console').innerHTML;
    const decoded = s => s.replace(/&quot;/g, '"').replace(/&#39;/g, "'");
    const removes = (html.match(/onclick="removePluginUnit\([^"]*\)"/g) || [])
        .map(a => decoded(a.slice('onclick="'.length, -1)));
    assert.ok(removes.length >= 3, 'the installed card must offer a per-unit removal, got ' + removes.length);
    assert.equal(removes[0], 'removePluginUnit("role","移动端安全测试")');
    sandbox.pluginConsoleBusyReset();
    vm.runInContext(removes[0], sandbox);
    for (let i = 0; i < 6; i++) await new Promise(r => setImmediate(r));
    const deletes = calls.filter(c => c.method === 'DELETE' && c.url.startsWith('/api/plugins/units/'));
    assert.equal(deletes.length, 1, JSON.stringify(calls.map(c => c.method + ' ' + c.url)));
    assert.equal(deletes[0].url, '/api/plugins/units/role/%E7%A7%BB%E5%8A%A8%E7%AB%AF%E5%AE%89%E5%85%A8%E6%B5%8B%E8%AF%95');
    assert.match(toasts[toasts.length - 1].msg, /已从能力包的选择中移除/);
});

// 摘除按钮的权限标记要对齐后端映射：DELETE /api/plugins/units/* 在中间件里是 plugins:write
// （与旁边的启停开关同级）。标成 plugins:install 会把按钮从恰好被 API 允许的操作者眼前藏起来。
test('the detach button is gated on plugins:write, matching the backend mapping', async () => {
    const { sandbox } = harness(sampleState, sampleCatalog);
    await sandbox.api.loadPluginConsole();
    const html = sandbox.document.getElementById('plugin-console').innerHTML;
    const tags = html.match(/<button[^>]*data-require-permission="[^"]*"[^>]*onclick="removePluginUnit\(/g) || [];
    assert.ok(tags.length > 0, 'no detach button rendered');
    for (const tag of tags) {
        assert.match(tag, /data-require-permission="plugins:write"/,
            'the detach marker drifted from the backend mapping (plugins:write): ' + tag);
    }
});

// 类型筛选是"只给我看技能"的那个开关：跑真实 onclick 文本，只看剩下的行。
test('the kind filter narrows the rows to one kind', async () => {
    const { sandbox } = harness(sampleState, sampleCatalog);
    await sandbox.api.loadPluginConsole();
    const decoded = s => s.replace(/&quot;/g, '"').replace(/&#39;/g, "'");
    // 整个控制台的 HTML 在这里是字符串（harness 不解析 DOM），筛选条与两张列表都在其中。
    const chips = (sandbox.document.getElementById('plugin-console').innerHTML
        .match(/onclick="applyPluginKindFilter\([^"]*\)"/g) || []).map(a => decoded(a.slice('onclick="'.length, -1)));
    const skillChip = chips.find(a => a.includes('"skill"'));
    assert.ok(skillChip, 'the kind chips must include one per kind present: ' + chips.join(' | '));
    vm.runInContext(skillChip, sandbox);
    // 局部重渲染写进的是各面板节点，harness 不解析 DOM，所以整页重绘一次再读那张字符串。
    sandbox.window.renderPluginConsole();
    const html = sandbox.document.getElementById('plugin-console').innerHTML;
    assert.match(html, /llm-output-boundaries/, 'the skill row must survive the filter');
    assert.ok(!/报告撰写/.test(html), 'rows of other kinds must be filtered out');
    assert.ok(!/移动端安全测试角色包/.test(html), 'a pack with nothing of that kind must disappear');
});
