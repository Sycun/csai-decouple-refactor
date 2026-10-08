// 能力包控制台：装 / 摘 / 启停都走 /api/plugins，读的是服务端那张能力表。
// 页面上每个单元都显式标出"运行路径是否真的在读它"（served）与没在读的原因，
// 避免"已安装"被读成"已生效"。
//
// 同一个原则贯穿这一页的每个新增字段：卡片先给"装下去会发生什么"（能力预览），
// 已装行给出来源与是否命中撤销列表，升级给单元差异，回滚只列真实存在的快照——
// 每一句都来自服务端读盘后的答复，不做任何推断。
function pluginsT(key, opts) {
    const k = 'plugins.' + key;
    return typeof window.t === 'function' ? window.t(k, opts) : k;
}

let pluginConsoleState = null;
let pluginConsoleCatalog = null;
let pluginConsoleBusy = false;
// 筛选词留在模块级：一次变更会整页重渲染，输入框重建后仍要显示同一个词。
let pluginConsoleFilter = '';
// The operator's pending unit choices, keyed "<pack>|<unit>". Module-level for the same reason the
// filter word is: a re-render rebuilds every checkbox, and what the operator ticked has to survive
// that. Keyed by identity rather than by row position so a card that moves cannot carry one pack's
// choice onto another's unit. Cleared on every successful fetch: once the server has answered, the
// installed/not-installed facts in it are the truth and a stale tick would contradict them.
let pluginUnitChoices = {};
// Kind filter for the unit rows ("skill" shows only skill rows). Kept separate from the text filter
// so "只看技能" and a search word can be combined.
let pluginKindFilter = '';
// 当前标签（可安装 / 已安装）同样留在模块级：变更后的重渲染要落回用户点开的那一页。
let pluginConsoleTab = 'available';

function isPluginConsoleActive() {
    const page = document.getElementById('page-plugins-management');
    return !!(page && page.classList.contains('active'));
}

async function loadPluginConsole() {
    if (pluginConsoleBusy) return;
    pluginConsoleBusy = true;
    try {
        await fetchPluginConsole();
    } finally {
        pluginConsoleBusy = false;
    }
}

// fetchPluginConsole is split from the entry point because a mutation reloads the table while it
// still holds the busy flag: calling the guarded version there would return without refreshing, and
// the console would keep showing the pre-mutation state.
async function fetchPluginConsole() {
    const listEl = document.getElementById('plugin-console');
    if (!listEl) return;
    // 能力表可能因装/卸/开关而变（比如加入或移走「多代理编排包」的模式声明）：顺带让对话
    // 模式目录重新拉取，对话页与 WebShell 的选择器无需刷新页面即可跟随（changed 事件驱动）。
    if (typeof window.csaiAgentModes !== 'undefined' && typeof window.csaiAgentModes.refresh === 'function') {
        window.csaiAgentModes.refresh();
    }
    listEl.innerHTML = '<div class="empty-state">' + escapeHtml(pluginsT('loading')) + '</div>';
    try {
        const [stateResp, catalogResp] = await Promise.all([
            apiFetch('/api/plugins'),
            apiFetch('/api/plugins/available'),
        ]);
        if (!stateResp.ok || !catalogResp.ok) {
            throw new Error('HTTP ' + stateResp.status + ' / ' + catalogResp.status);
        }
        pluginConsoleState = await stateResp.json();
        pluginConsoleCatalog = await catalogResp.json();
        // The fresh answer is the truth about which units are installed; a tick left over from
        // before the mutation would draw a checkbox that disagrees with the table it was just
        // read from.
        pluginUnitChoices = {};
        renderPluginConsole();
    } catch (err) {
        listEl.innerHTML = '<div class="empty-state">' +
            escapeHtml(pluginsT('loadFailed')) + ': ' + escapeHtml(err.message) + '</div>';
    }
}

// A successful mutation changes two things at once: this console's table, and every other page's
// in-memory copy of the capability lists. The chat page rendered its role sidebar once at page load
// and the webshell page keeps its own copy of /api/roles - refreshing only the console is why a
// freshly installed role stayed invisible until the whole browser page was reloaded. Both copies
// are re-read here, from the page that caused the change, so "装完即生效" holds on screen and not
// only in the table.
async function reloadConsoleAfterMutation() {
    await fetchPluginConsole();
    if (typeof loadRoles === 'function') {
        loadRoles();
    }
    if (typeof wsLoadRoles === 'function') {
        wsLoadRoles();
    }
    // The chat page's @ tool list refetches when this flag is set; a pack can add or remove
    // callable tools without the role changing, so the invalidation cannot wait for a role switch.
    window._mentionToolsRoleChanged = true;
}

function renderPluginConsole() {
    const listEl = document.getElementById('plugin-console');
    if (!listEl || !pluginConsoleState) return;
    const state = pluginConsoleState;
    const catalog = (pluginConsoleCatalog && pluginConsoleCatalog.bundles) || [];

    const parts = [];
    parts.push(renderPluginSummary(state));
    // The bar carries an id so a filter keystroke can repaint it (the active kind chip changes)
    // without rebuilding the text input and dropping the caret mid-word.
    parts.push('<div id="plugin-filter-bar">' + renderPluginFilterBar(catalog) + '</div>');

    const sections = pluginSectionsHtml();
    // 行为对齐系统设置：两个列表是标签，点哪个显示哪个，默认停在「可安装」。
    parts.push(renderPluginTabs());
    // The two panels carry ids so a filter keystroke can re-render just their content: replacing
    // the whole console would rebuild the input element and drop focus mid-word.
    parts.push(renderPluginPanel('available', sections.available));
    parts.push(renderPluginPanel('installed', sections.installed));

    parts.push(renderPluginSection(pluginsT('standaloneTitle'), (state.standalone || []).length
        ? renderPluginUnitTable(state.standalone)
        : '<div class="empty-state">' + escapeHtml(pluginsT('nothingStandalone')) + '</div>'));

    // 信任面板与单元行上的 已撤销 标记同源：都读 state.revocations。没有这一块，
    // "执行路径会拒绝它"这件事对运维者就只存在于日志里。
    parts.push(renderPluginSection(pluginsT('revocationsTitle'), renderRevocations(state.revocations)));

    const drift = state.drift || [];
    parts.push(renderPluginSection(pluginsT('driftTitle'), drift.length
        ? '<ul class="plugin-drift-list">' + drift.map(d =>
            '<li><code>' + escapeHtml(d) + '</code></li>').join('') + '</ul>'
        : '<div class="empty-state">' + escapeHtml(pluginsT('noDrift')) + '</div>'));

    listEl.innerHTML = parts.join('');
    if (typeof window.applyRBACToUI === 'function') {
        window.applyRBACToUI(listEl);
    }
}

// pluginSectionsHtml builds the two filtered lists. Both the full render and the filter path go
// through it, so a card can never render differently depending on how it got there.
function pluginSectionsHtml() {
    const state = pluginConsoleState;
    const catalog = (pluginConsoleCatalog && pluginConsoleCatalog.bundles) || [];
    const installedById = new Map((state.bundles || []).map(b => [b.id, b]));
    const catalogById = new Map(catalog.map(b => [b.id, b]));

    const visibleCatalog = catalog.filter(pluginMatchesFilter);
    // 货架列的是目录里的一切：未装的包是"可安装"，已装的包是"可调整选择"（补装、减装、勾到只剩
    // 一个单元都在同一张卡上完成）。旧的过滤把已装的包从这里藏起来，于是"想从包里再补一个技能"
    // 没有任何入口——按单元挑的前提是那张卡一直看得见。
    const availableHtml = visibleCatalog.length
        ? visibleCatalog.map(b => renderPluginAvailableCard(b, installedById.get(b.id))).join('')
        : '<div class="empty-state">' + escapeHtml(pluginConsoleFilter || pluginKindFilter ? pluginsT('filterEmpty') : pluginsT('nothingAvailable')) + '</div>';

    const visibleInstalled = (state.bundles || []).filter(pluginMatchesFilter);
    const installedHtml = visibleInstalled.length
        ? visibleInstalled.map(b => renderPluginInstalledCard(b, catalogById.get(b.id))).join('')
        : '<div class="empty-state">' + escapeHtml(pluginConsoleFilter || pluginKindFilter ? pluginsT('filterEmpty') : pluginsT('nothingInstalled')) + '</div>';

    return { available: availableHtml, installed: installedHtml };
}

// The canonical kind order, so the filter chips read the same way as the bundle kinds do
// everywhere else (roles, agents, skills, recipes, MCP, modes, plugins).
const pluginKindOrder = ['role', 'agent', 'skill', 'tool', 'mcp', 'mode', 'plugin'];

function packMatchesText(pack) {
    const hay = [
        pack.id, pack.name, pack.description, pack.author,
        (pack.categories || []).join(' '),
    ].filter(Boolean).join(' ').toLowerCase();
    return hay.indexOf(pluginConsoleFilter.toLowerCase()) >= 0;
}

function unitMatchesText(unit) {
    const hay = [unit.name, unit.id, unit.kind, unitKindLabel(unit.kind)]
        .filter(Boolean).join(' ').toLowerCase();
    return hay.indexOf(pluginConsoleFilter.toLowerCase()) >= 0;
}

// pluginMatchesFilter decides whether a card is listed at all: the kind filter needs something to
// show inside it, and the word has to match the pack or one of its units - typing a skill's name
// should find the pack that ships it, not only the pack whose title happens to contain the word.
function pluginMatchesFilter(pack) {
    if (!pack) return false;
    if (pluginKindFilter && !(pack.units || []).some(u => u.kind === pluginKindFilter)) return false;
    if (!pluginConsoleFilter) return true;
    if (packMatchesText(pack)) return true;
    return (pack.units || []).some(unitMatchesText);
}

// pluginVisibleUnits narrows the rows inside a card. A word that matched the pack keeps every row
// (the operator is looking for the pack); a word that matched a unit keeps only those rows, so the
// one skill it names is not buried under the rest of the manifest.
function pluginVisibleUnits(pack) {
    const units = (pack && pack.units) || [];
    const kindOk = u => !pluginKindFilter || u.kind === pluginKindFilter;
    if (!pluginConsoleFilter || packMatchesText(pack)) return units.filter(kindOk);
    return units.filter(u => kindOk(u) && unitMatchesText(u));
}

// applyPluginFilter is the input handler: it re-renders only the two list sections, so the text
// field keeps its focus and the operator can keep typing.
function applyPluginFilter(query) {
    pluginConsoleFilter = String(query == null ? '' : query).trim();
    rerenderPluginLists();
}

function renderPluginFilterBar(catalog) {
    const categories = [];
    const kinds = [];
    catalog.forEach(b => {
        (b && b.categories ? b.categories : []).forEach(c => {
            if (categories.indexOf(c) < 0) categories.push(c);
        });
        ((b && b.units) || []).forEach(u => {
            if (kinds.indexOf(u.kind) < 0) kinds.push(u.kind);
        });
    });
    kinds.sort((a, b) => pluginKindOrder.indexOf(a) - pluginKindOrder.indexOf(b));
    const chips = categories.length
        ? '<div class="plugin-filter-categories">' + categories.map(c =>
            '<button type="button" class="plugin-chip plugin-filter-chip" onclick="applyPluginFilter(' +
            escapeAttr(JSON.stringify(c)) + ')">' + escapeHtml(c) + '</button>').join('') + '</div>'
        : '';
    // The kind chips are the "just show me the skills" control: with a shelf of seventeen packs,
    // finding the one skill among them is the difference between browsing and searching.
    const kindChips = kinds.length
        ? '<div class="plugin-filter-kinds">' +
            renderPluginKindChip('', pluginsT('kindFilterAll')) +
            kinds.map(k => renderPluginKindChip(k, unitKindLabel(k))).join('') + '</div>'
        : '';
    return '<div class="plugin-filter">' +
        '<input id="plugin-filter-input" type="text" class="plugin-filter-input" placeholder="' +
        escapeAttr(pluginsT('filterPlaceholder')) + '" value="' + escapeAttr(pluginConsoleFilter) +
        '" oninput="applyPluginFilter(this.value)">' +
        chips + kindChips + '</div>';
}

function renderPluginKindChip(kind, label) {
    const active = pluginKindFilter === kind;
    return '<button type="button" class="plugin-chip plugin-filter-chip' + (active ? ' is-active' : '') +
        '" onclick="applyPluginKindFilter(' + escapeAttr(JSON.stringify(kind)) + ')">' +
        escapeHtml(label) + '</button>';
}

// applyPluginKindFilter re-renders the two lists exactly as the text filter does, so a chip click
// and a keystroke cannot produce two different pictures of the same state.
function applyPluginKindFilter(kind) {
    pluginKindFilter = String(kind == null ? '' : kind);
    rerenderPluginLists();
}

// rerenderPluginLists repaints the filter bar and both lists from the state already in memory. No
// request goes out: what changed is what is on screen, not what is installed.
function rerenderPluginLists() {
    const listEl = document.getElementById('plugin-console');
    if (!listEl || !pluginConsoleState) return;
    const catalog = (pluginConsoleCatalog && pluginConsoleCatalog.bundles) || [];
    const bar = document.getElementById('plugin-filter-bar');
    if (bar) bar.innerHTML = renderPluginFilterBar(catalog);
    const sections = pluginSectionsHtml();
    const available = document.getElementById('plugin-section-available');
    const installed = document.getElementById('plugin-section-installed');
    if (available) available.innerHTML = sections.available;
    if (installed) installed.innerHTML = sections.installed;
    if (typeof window.applyRBACToUI === 'function') {
        window.applyRBACToUI(listEl);
    }
}

function renderPluginSummary(state) {
    const served = (state.servedKinds || []).slice().sort();
    return '<div class="plugin-summary">' +
        '<div class="plugin-summary-item"><span>' + escapeHtml(pluginsT('bundlesRoot')) + '</span>' +
        '<code>' + escapeHtml(state.bundlesRoot || '-') + '</code></div>' +
        '<div class="plugin-summary-item"><span>' + escapeHtml(pluginsT('generation')) + '</span>' +
        '<strong>' + escapeHtml(String(state.generation == null ? '-' : state.generation)) + '</strong></div>' +
        '<div class="plugin-summary-item"><span>' + escapeHtml(pluginsT('servedKinds')) + '</span>' +
        (served.length
            ? served.map(k => '<span class="plugin-chip plugin-chip-served">' + escapeHtml(k) + '</span>').join('')
            : '<span class="plugin-chip">-</span>') + '</div>' +
        '</div>';
}

function renderPluginSection(title, body) {
    return '<section class="plugin-section"><h3 class="plugin-section-title">' +
        escapeHtml(title) + '</h3>' + body + '</section>';
}

// The two lists are tabs instead of two stacked sections: the console stays short, and an install
// that moves a card from one list to the other lands back on the tab the operator chose. The
// markup reuses the dashboard's segmented-tab classes so both screens read as the same control.
function renderPluginTabs() {
    return '<nav class="dashboard-feed-tabs" role="tablist" aria-label="' +
        escapeAttr(pluginsT('tabsAria')) + '">' +
        renderPluginTabButton('available') + renderPluginTabButton('installed') + '</nav>';
}

function renderPluginTabButton(tab) {
    const active = pluginConsoleTab === tab;
    return '<button type="button" class="dashboard-feed-tab' + (active ? ' is-active' : '') + '" ' +
        'role="tab" id="plugin-tab-' + tab + '" aria-selected="' + (active ? 'true' : 'false') + '" ' +
        'aria-controls="plugin-section-' + tab + '" onclick="switchPluginTab(' +
        escapeAttr(JSON.stringify(tab)) + ')">' +
        escapeHtml(tab === 'available' ? pluginsT('availableTitle') : pluginsT('installedTitle')) + '</button>';
}

function renderPluginPanel(tab, body) {
    return '<section class="plugin-section" id="plugin-section-' + tab + '" role="tabpanel" ' +
        'aria-labelledby="plugin-tab-' + tab + '"' + (pluginConsoleTab === tab ? '' : ' hidden') + '>' +
        body + '</section>';
}

// Like the settings nav, clicking a tab only flips what is on screen - both lists were rendered
// either way, so no request goes out and the filter input keeps its text and caret.
function switchPluginTab(tab) {
    pluginConsoleTab = tab === 'installed' ? 'installed' : 'available';
    ['available', 'installed'].forEach(name => {
        const active = name === pluginConsoleTab;
        const panel = document.getElementById('plugin-section-' + name);
        if (panel) panel.hidden = !active;
        const button = document.getElementById('plugin-tab-' + name);
        if (button) {
            button.classList.toggle('is-active', active);
            button.setAttribute('aria-selected', active ? 'true' : 'false');
        }
    });
}

function unitKindLabel(kind) {
    return pluginsT('kind.' + kind);
}

function servedMark(unit) {
    if (unit.served) {
        return '<span class="plugin-chip plugin-chip-served">' + escapeHtml(pluginsT('served')) + '</span>';
    }
    const reason = unit.reason || pluginsT('notServedDefault');
    // reason goes into an attribute; the shared escapeHtml mirrors the browser's innerHTML rules,
    // which leave a double quote alone - that would break out of title="...".
    return '<span class="plugin-chip plugin-chip-unserved" title="' + escapeAttr(reason) + '">' +
        escapeHtml(pluginsT('notServed')) + '</span>';
}

// A plugin unit is the one kind that runs as its own process, so its row says what the live host
// holds: running or not, and how many times the child has had to be restarted. Without this the
// console would show a switch and a served flag, and the operator's third-party binary could be
// crash-looping with nothing on screen to say so.
function pluginRuntimeNote(unit) {
    if (unit.kind !== 'plugin') return '';
    const rows = pluginConsoleState && Array.isArray(pluginConsoleState.pluginHost)
        ? pluginConsoleState.pluginHost : [];
    const row = rows.filter(item => item && item.domain === unit.name)[0];
    if (!row) return '<span class="plugin-chip">' + escapeHtml(pluginsT('runtimeNone')) + '</span>';
    const label = pluginsT(row.running ? 'runtimeRunning' : 'runtimeStopped');
    // Composed from a prefix and a suffix rather than interpolated: this console's t() does not
    // substitute variables - a browser returned "重启 {count} 次" verbatim for a key written that
    // way, and {n} too - so the number has to be put in by the caller, and the two halves are what
    // keeps the word order right in both languages.
    const restarts = Number(row.restarts) > 0
        ? ' · ' + pluginsT('runtimeRestartsPrefix') + Number(row.restarts) + pluginsT('runtimeRestartsSuffix')
        : '';
    return '<span class="plugin-chip">' + escapeHtml(label + restarts) + '</span>';
}

// unitTrustNote is where a unit stops being anonymous: the digest taken when it was installed, who
// published it (executable kinds), and whether the block list currently refuses it. Rendered only
// for pack-owned units - the built-in tables are ours and a digest chip on all 142 rows would bury
// the third-party ones this exists for.
function unitTrustNote(unit) {
    const chips = [];
    if (unit.bundle && unit.digest) {
        chips.push('<code class="plugin-chip plugin-chip-digest" title="' + escapeAttr(pluginsT('digestTitle')) +
            '">' + escapeHtml(String(unit.digest).slice(0, 12)) + '…</code>');
    }
    if (unit.publisher) {
        chips.push('<span class="plugin-chip">' + escapeHtml(pluginsT('publisherPrefix') + unit.publisher) + '</span>');
    }
    if (unit.revoked) {
        chips.push('<span class="plugin-chip plugin-chip-danger" title="' + escapeAttr(pluginsT('revokedTitle')) +
            '">' + escapeHtml(pluginsT('revokedBadge')) + '</span>');
    }
    return chips.length ? '<div class="plugin-unit-trust">' + chips.join('') + '</div>' : '';
}

function unitCells(unit) {
    return '<td><span class="plugin-kind">' + escapeHtml(unitKindLabel(unit.kind)) + '</span>' +
        pluginRuntimeNote(unit) + '</td>' +
        '<td><code>' + escapeHtml(unit.name) + '</code>' + unitTrustNote(unit) + '</td>' +
        '<td>' + servedMark(unit) + '</td>';
}

function renderPluginUnitTable(units) {
    const rows = units.map(u => {
        const bundleCell = u.bundle ? '<code>' + escapeHtml(u.bundle) + '</code>' : '-';
        return '<tr>' + unitCells(u) +
            '<td>' + bundleCell + '</td>' +
            '<td>' + unitSwitch(u) + '</td></tr>';
    }).join('');
    return '<table class="plugin-table"><thead><tr>' +
        '<th>' + escapeHtml(pluginsT('colKind')) + '</th>' +
        '<th>' + escapeHtml(pluginsT('colName')) + '</th>' +
        '<th>' + escapeHtml(pluginsT('colServed')) + '</th>' +
        '<th>' + escapeHtml(pluginsT('colBundle')) + '</th>' +
        '<th>' + escapeHtml(pluginsT('colEnabled')) + '</th>' +
        '</tr></thead><tbody>' + rows + '</tbody></table>';
}

function unitSwitch(unit) {
    // Three positional arguments, each quoted separately: emitting one JSON array would hand the
    // whole array to the first parameter, and the browser would POST
    // /units/tool,semgrep,false/undefined/enabled while every unit test still looked green.
    const args = [unit.kind, unit.name, !unit.enabled]
        .map(v => typeof v === 'string' ? escapeAttr(JSON.stringify(v)) : String(v === true))
        .join(',');
    return '<button class="btn-secondary btn-sm" data-require-permission="plugins:write" ' +
        'onclick="setPluginUnitEnabled(' + args + ')">' +
        escapeHtml(unit.enabled ? pluginsT('disable') : pluginsT('enable')) + '</button>';
}

function classLabel(cls) {
    const known = { readonly: 'classReadonly', mutating: 'classMutating', destructive: 'classDestructive' };
    return known[cls] ? pluginsT(known[cls]) : cls;
}

function classChipClass(cls) {
    if (cls === 'destructive') return ' plugin-chip-danger';
    if (cls === 'mutating') return ' plugin-chip-warn';
    return '';
}

// renderPreviewBadges is the "what would installing this register" strip: declared classes, the
// live-code flag, and the two counts that are warnings rather than features (recipes that will
// fail closed, units whose metadata could not be read at all).
function renderPreviewBadges(preview) {
    preview = preview || {};
    const classes = preview.classes || {};
    const chips = [];
    Object.keys(classes).sort().forEach(name => {
        chips.push('<span class="plugin-chip' + classChipClass(name) + '">' +
            escapeHtml(classLabel(name)) + ' × ' + Number(classes[name]) + '</span>');
    });
    if (preview.liveCodeUnits > 0) {
        chips.push('<span class="plugin-chip plugin-chip-warn">' + escapeHtml(pluginsT('liveCodeChip')) + '</span>');
    }
    if (preview.undeclaredUnits > 0) {
        chips.push('<span class="plugin-chip plugin-chip-warn">' +
            escapeHtml(pluginsT('undeclaredPrefix') + Number(preview.undeclaredUnits) + pluginsT('undeclaredSuffix')) + '</span>');
    }
    if (preview.problemUnits > 0) {
        chips.push('<span class="plugin-chip plugin-chip-danger">' +
            escapeHtml(pluginsT('problemPrefix') + Number(preview.problemUnits) + pluginsT('problemSuffix')) + '</span>');
    }
    return chips.length ? '<p class="plugin-card-badges">' + chips.join('') + '</p>' : '';
}

function renderCatalogueMeta(pack) {
    const parts = [];
    if (pack.author) parts.push(pluginsT('authorPrefix') + pack.author);
    if (pack.homepage) parts.push(pluginsT('homepagePrefix') + pack.homepage);
    if (pack.license) parts.push(pluginsT('licensePrefix') + pack.license);
    if (pack.compatibility) parts.push(pluginsT('compatibilityPrefix') + pack.compatibility);
    let html = parts.length
        ? '<p class="plugin-card-meta-line">' + escapeHtml(parts.join(' · ')) + '</p>'
        : '';
    if (pack.categories && pack.categories.length) {
        html += '<p class="plugin-card-badges">' + pack.categories.map(c =>
            '<span class="plugin-chip">' + escapeHtml(c) + '</span>').join('') + '</p>';
    }
    return html;
}

function packVersionDiffers(catalogPack, installed) {
    return !!(catalogPack && installed && catalogPack.version &&
        String(catalogPack.version) !== String(installed.version || ''));
}

// unitDiff counts what an upgrade would change, from digests the catalogue and the table both
// carry - no file is re-read here, and the same numbers feed the card and the confirm dialog.
function unitDiff(installedUnits, catalogUnits) {
    const oldById = new Map((installedUnits || []).map(u => [u.id, u]));
    const newById = new Map((catalogUnits || []).map(u => [u.id, u]));
    let added = 0, removed = 0, changed = 0;
    newById.forEach((u, id) => {
        const before = oldById.get(id);
        if (!before) added++;
        else if ((u.digest || '') !== (before.digest || '')) changed++;
    });
    oldById.forEach((u, id) => {
        if (!newById.has(id)) removed++;
    });
    return { added: added, removed: removed, changed: changed };
}

function diffSummaryText(diff) {
    const bits = [];
    if (diff.added) bits.push(pluginsT('diffAddedPrefix') + diff.added + pluginsT('diffAddedSuffix'));
    if (diff.removed) bits.push(pluginsT('diffRemovedPrefix') + diff.removed + pluginsT('diffRemovedSuffix'));
    if (diff.changed) bits.push(pluginsT('diffChangedPrefix') + diff.changed + pluginsT('diffChangedSuffix'));
    return bits.join(' · ');
}

// ---- unit choices ------------------------------------------------------------------------
//
// A pack is a way to ship units together, not a rule that they must be installed together, so the
// shelf card is a checklist: tick the units this machine should have and the same button installs,
// adds or takes away exactly that set. The server treats the ticked list as the desired state, so
// re-sending an unchanged selection changes nothing.

function unitChoiceKey(packID, unitID) { return packID + '|' + unitID; }

// unitChoiceIsOn is what a checkbox shows when the operator has not touched it. A pack nobody
// installed defaults to every unit ticked, so a plain click still means "the whole pack"; a pack
// that is installed defaults to the units the server says are in the table, so opening the card
// cannot quietly widen what is installed.
function unitChoiceIsOn(pack, unit, installed) {
    const key = unitChoiceKey(pack.id, unit.id);
    if (Object.prototype.hasOwnProperty.call(pluginUnitChoices, key)) {
        return !!pluginUnitChoices[key];
    }
    return installed ? unit.installed !== false : true;
}

// chosenUnitIDs is the selection this card would send. A unit whose identity another holder owns is
// never included: installing it is refused by name, so ticking it for the operator would only
// produce a failure they did not ask for.
function chosenUnitIDs(pack, installed) {
    const out = [];
    (pack.units || []).forEach(u => {
        if (u.conflict) return;
        if (unitChoiceIsOn(pack, u, installed)) out.push(u.id);
    });
    return out;
}

function installedUnitIDs(installedPack) {
    return ((installedPack && installedPack.units) || [])
        .filter(u => u.installed !== false)
        .map(u => u.id);
}

function selectableUnitCount(pack) {
    return (pack.units || []).filter(u => !u.conflict).length;
}

function unitConflictNote(unit) {
    if (!unit.conflict) return '';
    return '<div class="plugin-unit-trust"><span class="plugin-chip plugin-chip-danger">' +
        escapeHtml(unit.conflict) + '</span></div>';
}

function renderUnitChoiceTable(pack, visible, chosen) {
    if (!visible.length) {
        return '<div class="empty-state">' + escapeHtml(pluginsT('filterEmpty')) + '</div>';
    }
    const rows = visible.map(u => {
        const on = !!u.conflict || chosen.indexOf(u.id) >= 0;
        const box = '<input type="checkbox" class="plugin-unit-check"' + (on ? ' checked' : '') +
            (u.conflict ? ' disabled' : '') + ' data-require-permission="plugins:install" ' +
            'onchange="togglePluginUnitChoice(' + escapeAttr(JSON.stringify(pack.id)) + ',' +
            escapeAttr(JSON.stringify(u.id)) + ',this.checked)">';
        return '<tr' + (u.conflict ? ' class="plugin-unit-conflict"' : '') + '>' +
            '<td class="plugin-unit-check-cell">' + box + '</td>' +
            '<td><span class="plugin-kind">' + escapeHtml(unitKindLabel(u.kind)) + '</span>' +
            pluginRuntimeNote(u) + '</td>' +
            '<td><code>' + escapeHtml(u.name) + '</code>' + unitTrustNote(u) + unitConflictNote(u) + '</td>' +
            '<td>' + (u.installed
                ? '<span class="plugin-chip plugin-chip-served">' + escapeHtml(pluginsT('unitInstalled')) + '</span>'
                : '<span class="plugin-chip">' + escapeHtml(pluginsT('unitNotInstalled')) + '</span>') + '</td>' +
            '</tr>';
    }).join('');
    return '<table class="plugin-table plugin-choice-table"><thead><tr>' +
        '<th class="plugin-unit-check-cell"></th>' +
        '<th>' + escapeHtml(pluginsT('colKind')) + '</th>' +
        '<th>' + escapeHtml(pluginsT('colName')) + '</th>' +
        '<th>' + escapeHtml(pluginsT('colUnitState')) + '</th>' +
        '</tr></thead><tbody>' + rows + '</tbody></table>';
}

// selectionDeltaLine states what clicking would change, before it is clicked: the units that would
// arrive and the ones that would leave. A reconcile that quietly drops a unit is the one outcome
// the console must not hide.
function selectionDeltaLine(toAdd, toDrop) {
    if (!toAdd.length && !toDrop.length) return '';
    const bits = [];
    if (toAdd.length) bits.push(pluginsT('willInstallPrefix') + toAdd.length + pluginsT('willInstallSuffix'));
    if (toDrop.length) bits.push(pluginsT('willRemovePrefix') + toDrop.join('、') + pluginsT('willRemoveSuffix'));
    return '<p class="plugin-card-meta-line">' + escapeHtml(bits.join(' · ')) + '</p>';
}

function renderPluginAvailableCard(pack, installed) {
    const units = pack.units || [];
    const kinds = {};
    units.forEach(u => { kinds[u.kind] = (kinds[u.kind] || 0) + 1; });
    const kindText = Object.keys(kinds).sort().map(k => unitKindLabel(k) + ' × ' + kinds[k]).join('、');
    const upgrade = packVersionDiffers(pack, installed);
    const chosen = chosenUnitIDs(pack, installed);
    const live = installedUnitIDs(installed);
    const count = selectableUnitCount(pack);
    const toAdd = chosen.filter(id => live.indexOf(id) < 0);
    const toDrop = live.filter(id => chosen.indexOf(id) < 0);
    const broken = pack.error
        ? '<div class="plugin-error">' + escapeHtml(pluginsT('manifestBroken')) + ': ' +
          escapeHtml(pack.error) + '</div>'
        : '';
    const installArg = escapeAttr(JSON.stringify(pack.id));
    const versionLine = escapeHtml(pack.id) +
        (pack.version ? ' · v' + escapeHtml(pack.version) : '') +
        (upgrade ? ' · ' + escapeHtml(pluginsT('installedVersionPrefix') + installed.version) : '');
    const upgradeLine = upgrade
        ? '<p class="plugin-card-meta-line">' + escapeHtml(
            pluginsT('upgradeAvailablePrefix') + pack.version + pluginsT('upgradeAvailableSuffix')) + '</p>'
        : '';
    let diffLine = '';
    if (upgrade) {
        const text = diffSummaryText(unitDiff(installed.units, units));
        if (text) {
            diffLine = '<p class="plugin-card-meta-line">' + escapeHtml(pluginsT('differencesLabel') + text) + '</p>';
        }
    }
    const installedState = installed
        ? '<p class="plugin-card-meta-line">' + escapeHtml(
            pluginsT('installedUnitsPrefix') + live.length + '/' + units.length) + '</p>'
        : '';
    const buttonLabel = (installed ? (upgrade ? pluginsT('upgrade') : pluginsT('applySelection'))
        : pluginsT('install')) + ' (' + chosen.length + '/' + count + ')';
    return '<article class="plugin-card' + (pack.error ? ' plugin-card-broken' : '') + '">' +
        '<header class="plugin-card-head">' +
        '<h4>' + escapeHtml(pack.name || pack.id) + '</h4>' +
        '<span class="plugin-card-meta">' + versionLine + '</span>' +
        '</header>' +
        (pack.description ? '<p class="plugin-card-desc">' + escapeHtml(pack.description) + '</p>' : '') +
        renderCatalogueMeta(pack) +
        (kindText ? '<p class="plugin-card-units">' + escapeHtml(kindText) + '</p>' : '') +
        renderPreviewBadges(pack.preview) +
        upgradeLine + diffLine + installedState +
        broken +
        (pack.error ? '' :
            renderUnitChoiceTable(pack, pluginVisibleUnits(pack), chosen) +
            selectionDeltaLine(toAdd, toDrop) +
            '<div class="plugin-card-actions plugin-selection-actions">' +
            '<button class="btn-secondary btn-sm" data-require-permission="plugins:install" ' +
            'onclick="selectAllPluginUnits(' + installArg + ',true)">' +
            escapeHtml(pluginsT('selectAll')) + '</button>' +
            '<button class="btn-secondary btn-sm" data-require-permission="plugins:install" ' +
            'onclick="selectAllPluginUnits(' + installArg + ',false)">' +
            escapeHtml(pluginsT('selectNone')) + '</button>' +
            '<button class="btn-primary btn-sm" data-require-permission="plugins:install" ' +
            (chosen.length ? '' : 'disabled title="' + escapeAttr(pluginsT('selectAtLeastOne')) + '" ') +
            'onclick="installPluginBundle(' + installArg + ')">' +
            escapeHtml(buttonLabel) + '</button></div>') +
        '</article>';
}

function renderPluginInstalledCard(pack, catalogEntry) {
    const all = pack.units || [];
    // What is live, not what the manifest declares: since a pack can be installed partially, the
    // bundle view lists units that were never taken. Showing them here would read as "installed".
    const live = all.filter(u => u.installed !== false);
    const units = pluginVisibleUnits(pack).filter(u => u.installed !== false);
    const missing = all.length - live.length;
    const idArg = escapeAttr(JSON.stringify(pack.id));
    const upgradable = packVersionDiffers(catalogEntry, pack);
    const upgradeChip = upgradable
        ? '<p class="plugin-card-meta-line">' + escapeHtml(
            pluginsT('upgradeAvailablePrefix') + catalogEntry.version + pluginsT('upgradeAvailableSuffix')) + '</p>'
        : '';
    // 未装进来的单元不是没装过包，而是这个包还能补装：指路到货架那张卡，那里勾选。
    const missingChip = missing > 0
        ? '<p class="plugin-card-meta-line">' + escapeHtml(
            pluginsT('unitsLeftOutPrefix') + missing + pluginsT('unitsLeftOutSuffix')) + '</p>'
        : '';
    // The installed version itself is not a rollback target: restoring what is already live would
    // rewrite the directory for no observable change.
    const rollbacks = (pack.rollbacks || []).filter(v => String(v) !== String(pack.version || ''));
    let rollbackHtml = '';
    if (rollbacks.length) {
        rollbackHtml = '<div class="plugin-rollback"><span>' + escapeHtml(pluginsT('rollbackTitle')) + '</span>' +
            rollbacks.map(v =>
                '<button class="btn-secondary btn-sm" data-require-permission="plugins:install" ' +
                'onclick="rollbackPluginBundle(' + idArg + ',' + escapeAttr(JSON.stringify(v)) + ')">' +
                escapeHtml(pluginsT('rollbackButton') + ' v' + v) + '</button>').join('') + '</div>';
    }
    const rowsHtml = units.length
        ? '<table class="plugin-table"><thead><tr>' +
          '<th>' + escapeHtml(pluginsT('colKind')) + '</th>' +
          '<th>' + escapeHtml(pluginsT('colName')) + '</th>' +
          '<th>' + escapeHtml(pluginsT('colServed')) + '</th>' +
          '<th>' + escapeHtml(pluginsT('colEnabled')) + '</th>' +
          '</tr></thead><tbody>' +
          units.map(u => '<tr>' + unitCells(u) + '<td>' +
              '<div class="plugin-unit-actions">' + unitSwitch(u) + unitRemoveButton(u) + '</div></td></tr>').join('') +
          '</tbody></table>'
        : '<div class="empty-state">' + escapeHtml(pluginsT('filterEmpty')) + '</div>';
    return '<article class="plugin-card plugin-card-installed">' +
        '<header class="plugin-card-head">' +
        '<h4>' + escapeHtml(pack.name || pack.id) + '</h4>' +
        '<span class="plugin-card-meta">' + escapeHtml(pack.id) +
        (pack.version ? ' · v' + escapeHtml(pack.version) : '') +
        (pack.unitsTotal ? ' · ' + escapeHtml(pluginsT('installedUnitsPrefix') + live.length + '/' + pack.unitsTotal) : '') +
        '</span>' +
        '</header>' +
        (pack.description ? '<p class="plugin-card-desc">' + escapeHtml(pack.description) + '</p>' : '') +
        renderCatalogueMeta(pack) +
        upgradeChip + missingChip +
        rowsHtml +
        rollbackHtml +
        '<div class="plugin-card-actions">' +
        '<button class="btn-secondary btn-sm" data-require-permission="plugins:install" ' +
        'onclick="unplugPluginBundle(' + idArg + ')">' +
        escapeHtml(pluginsT('unplug')) + '</button></div>' +
        '</article>';
}

// unitRemoveButton takes one unit out of an installed pack. It is the per-unit counterpart of the
// unplug button and is deliberately worded as a removal from the pack, not a delete: the file stays
// where it is and the card in the shelf can put the unit back.
function unitRemoveButton(unit) {
    const args = [unit.kind, unit.name]
        .map(v => escapeAttr(JSON.stringify(v))).join(',');
    return '<button class="btn-secondary btn-sm" data-require-permission="plugins:install" ' +
        'onclick="removePluginUnit(' + args + ')">' +
        escapeHtml(pluginsT('removeUnit')) + '</button>';
}

// renderRevocations shows what the client-enforced block list currently holds. Counts and the
// source file, not the full digest list: the list is what the execution path reads, and the panel
// exists so "why is X refused" has an answer on screen instead of in a log line.
function renderRevocations(revo) {
    if (!revo || !revo.loaded) {
        return '<div class="empty-state">' + escapeHtml(pluginsT('revocationsNone')) + '</div>';
    }
    const digests = revo.digests || [];
    const publishers = revo.publishers || [];
    let chips = '';
    if (publishers.length) {
        const shown = publishers.slice(0, 8).map(p =>
            '<span class="plugin-chip plugin-chip-danger">' + escapeHtml(p) + '</span>').join('');
        const rest = publishers.length > 8
            ? '<span class="plugin-chip">+' + (publishers.length - 8) + '</span>'
            : '';
        chips = '<div class="plugin-card-badges">' + shown + rest + '</div>';
    }
    return '<div class="plugin-summary">' +
        '<div class="plugin-summary-item"><span>' + escapeHtml(pluginsT('revocationsSource')) + '</span>' +
        '<code>' + escapeHtml(revo.source || '-') + '</code></div>' +
        '<div class="plugin-summary-item"><span>' + escapeHtml(pluginsT('revocationsDigestLabel')) + '</span>' +
        '<strong>' + digests.length + '</strong></div>' +
        '<div class="plugin-summary-item"><span>' + escapeHtml(pluginsT('revocationsPublisherLabel')) + '</span>' +
        '<strong>' + publishers.length + '</strong></div>' +
        '</div>' + chips;
}

// installConfirmText is the pre-click answer to "what will this do": units, classes, live code,
// and the recipes that will be refused at call time. Every count comes from the preview the
// server read off the pack's own files.
function installConfirmText(pack, installed, chosen) {
    const upgrade = packVersionDiffers(pack, installed);
    const lines = [];
    lines.push((upgrade ? pluginsT('confirmUpgradeHead') : pluginsT('confirmInstallHead')) +
        (pack.name || pack.id) + pluginsT('confirmInstallTail'));
    if (upgrade) {
        lines.push(pluginsT('confirmUpgradePrefix') + 'v' + installed.version + ' → v' + pack.version);
        const text = diffSummaryText(unitDiff(installed.units, pack.units));
        if (text) lines.push(pluginsT('differencesLabel') + text);
    }
    // 整包的选择不必再复述一遍"本次选择 3/3"；只有已经装过（这次是在调整）或者选的是子集时，
    // 这句话才是新信息。
    if (chosen && (installed || chosen.length !== selectableUnitCount(pack))) {
        lines.push(pluginsT('confirmSelectionPrefix') + chosen.length + '/' + selectableUnitCount(pack) +
            pluginsT('confirmSelectionSuffix'));
        // A reconcile can take units away; naming them in the dialog is the difference between
        // "install" and "install, minus something you forgot about".
        const toDrop = installedUnitIDs(installed).filter(id => chosen.indexOf(id) < 0);
        if (toDrop.length) lines.push(pluginsT('willRemovePrefix') + toDrop.join('、') + pluginsT('willRemoveSuffix'));
    }
    const preview = pack.preview || {};
    const classes = preview.classes || {};
    const parts = Object.keys(classes).sort().map(name => classLabel(name) + ' × ' + classes[name]);
    if (parts.length) lines.push(pluginsT('confirmClassesPrefix') + parts.join(' · '));
    if (preview.liveCodeUnits > 0) lines.push(pluginsT('confirmLiveCode'));
    if (preview.undeclaredUnits > 0) {
        lines.push(pluginsT('undeclaredPrefix') + preview.undeclaredUnits + pluginsT('undeclaredSuffix'));
    }
    if (preview.problemUnits > 0) {
        lines.push(pluginsT('problemPrefix') + preview.problemUnits + pluginsT('problemSuffix'));
    }
    return lines.join('\n');
}

// notify uses the platform's shared toast when it is loaded. It must never be the reason a
// mutation looks like it failed: the request already succeeded, and awaiting a toast helper that
// does not exist on this page threw and showed "install failed" over a successful install.
function notify(message, type) {
    if (typeof showNotification === 'function') {
        try {
            showNotification(message, type);
        } catch (e) {
            console.warn('notify', e);
        }
    }
}

async function runPluginRequest(method, url, body) {
    const resp = await apiFetch(url, {
        method: method,
        headers: { 'Content-Type': 'application/json' },
        body: body === undefined ? undefined : JSON.stringify(body),
    });
    const data = await resp.json().catch(() => ({}));
    if (!resp.ok) {
        throw new Error(data.error || ('HTTP ' + resp.status));
    }
    return data;
}

// unitSummaryText names what a pack actually brought, in the same shape the catalogue card counts
// it: the operator reads "角色 × 4、子代理 × 2、技能 × 2" at the toast where they clicked, instead
// of having to find the card that moved down the page. Counts come from the response's own bundle
// view, so the sentence is the server's, not a guess from what the click asked for.
function unitSummaryText(bundle) {
    // 只数装进去的：包视图列的是清单，部分安装时未选中的单元也在里面，数上它们等于把"装了 2 个"
    // 说成"装了 5 个"。
    const units = ((bundle && bundle.units) || []).filter(u => u.installed !== false);
    if (!units.length) return '';
    const kinds = {};
    units.forEach(u => { kinds[u.kind] = (kinds[u.kind] || 0) + 1; });
    const text = Object.keys(kinds).sort().map(k => unitKindLabel(k) + ' × ' + kinds[k]).join('、');
    return text ? ' · ' + text : '';
}

// togglePluginUnitChoice is a checkbox handler: it records the tick and repaints the lists. No
// request goes out - nothing about the machine has changed yet, and the click that does change it
// is the install button.
function togglePluginUnitChoice(packID, unitID, checked) {
    pluginUnitChoices[unitChoiceKey(packID, unitID)] = !!checked;
    rerenderPluginLists();
}

// selectAllPluginUnits is the "整包" / "全不选" pair. Conflicting units are skipped: they cannot be
// installed, so a select-all that ticked them would only produce a failure.
function selectAllPluginUnits(packID, on) {
    const pack = ((pluginConsoleCatalog && pluginConsoleCatalog.bundles) || [])
        .filter(b => b.id === packID)[0];
    if (!pack) return;
    (pack.units || []).forEach(u => {
        if (u.conflict) return;
        pluginUnitChoices[unitChoiceKey(packID, u.id)] = !!on;
    });
    rerenderPluginLists();
}

async function installPluginBundle(bundleId) {
    const catalog = (pluginConsoleCatalog && pluginConsoleCatalog.bundles) || [];
    const pack = catalog.filter(b => b.id === bundleId)[0];
    const installed = pluginConsoleState &&
        (pluginConsoleState.bundles || []).filter(b => b.id === bundleId)[0];
    const upgrade = packVersionDiffers(pack, installed);
    const chosen = pack ? chosenUnitIDs(pack, installed) : [];
    if (pack && !chosen.length) {
        notify(pluginsT('selectAtLeastOne'), 'error');
        return;
    }
    // Installing always asks first. Without catalogue data there is no preview to show, but
    // skipping the dialog would make "no information" look like "nothing to tell" - the same lie
    // the served flag exists to prevent.
    const text = pack
        ? installConfirmText(pack, installed, chosen)
        : pluginsT('confirmInstallHead') + bundleId + pluginsT('confirmInstallTail');
    if (!window.confirm(text)) return;
    await withPluginBusy(async () => {
        const body = { bundle: bundleId };
        // 勾选随请求一起走，但"勾满且没有冲突项"时退回不带 units 的旧形状：那正是整包请求，
        // 服务端也会把它记成"跟随目录"，少一个字段就少一种两边不一致的可能。没有目录信息时
        // （catalog 读不到）同样退回旧形状。
        if (pack && chosen.length && !selectionIsWholePack(pack, chosen)) body.units = chosen;
        const data = await runPluginRequest('POST', '/api/plugins/install', body);
        const base = upgrade ? pluginsT('upgradeDone')
            : (installed ? pluginsT('applySelectionDone')
                : (data.tools_rebuilt ? pluginsT('installWithTools') : pluginsT('installPlain')));
        notify(`${base} ${data.bundle && data.bundle.id ? data.bundle.id : bundleId}` +
            `${unitSummaryText(data.bundle)}${selectionDoneText(data)}${serverNote(data)}`, 'success');
        await reloadConsoleAfterMutation();
    }, upgrade ? 'upgrade' : (installed ? 'applySelection' : 'install'));
}

// selectionIsWholePack says whether the ticks cover every unit the pack declares and none of them is
// in conflict. That is the whole-pack request, and spelling it as the absence of a selection keeps
// the two spellings of one decision from drifting apart.
function selectionIsWholePack(pack, chosen) {
    const units = pack.units || [];
    return chosen.length === units.length && selectableUnitCount(pack) === units.length;
}

// selectionDoneText states what the selection did, not just that the request succeeded: a reconcile
// can take units out, and a toast that only says "done" would hide a unit leaving the table.
function selectionDoneText(data) {
    if (!data) return '';
    const parts = [];
    if (Array.isArray(data.units_removed) && data.units_removed.length) {
        parts.push(pluginsT('willRemovePrefix') + data.units_removed.join('、') + pluginsT('willRemoveSuffix'));
    }
    if (typeof data.units_installed === 'number' && typeof data.units_total === 'number') {
        parts.push(pluginsT('installedUnitsPrefix') + data.units_installed + '/' + data.units_total);
    }
    return parts.length ? ' · ' + parts.join(' · ') : '';
}

// removePluginUnit takes one unit out of a pack. The file stays on disk: the unit leaves the
// capability table and the install record, and the shelf card is where it can be put back. Removing
// the pack's last unit is performed as the uninstall it is, and the response says so.
async function removePluginUnit(kind, name) {
    const text = pluginsT('confirmRemoveUnitHead') + kind + '/' + name + pluginsT('confirmRemoveUnitTail');
    if (!window.confirm(text)) return;
    await withPluginBusy(async () => {
        const data = await runPluginRequest('DELETE',
            '/api/plugins/units/' + encodeURIComponent(kind) + '/' + encodeURIComponent(name));
        const tail = data.bundle_uninstalled ? ' · ' + pluginsT('removeUnitUninstalled') : '';
        notify(pluginsT('removeUnitDone') + tail + serverNote(data), 'success');
        await reloadConsoleAfterMutation();
    }, 'removeUnit');
}

// rollbackPluginBundle restores a snapshot the install path kept. The confirm names both the pack
// and the version because the operation overwrites the pack directory.
async function rollbackPluginBundle(bundleId, version) {
    const text = pluginsT('confirmRollbackHead') + bundleId + pluginsT('confirmRollbackMid') +
        'v' + version + pluginsT('confirmRollbackTail');
    if (!window.confirm(text)) return;
    await withPluginBusy(async () => {
        const data = await runPluginRequest('POST', '/api/plugins/install', {
            bundle: bundleId,
            from_version: version,
        });
        notify(pluginsT('rollbackDone') + ' ' + bundleId + unitSummaryText(data.bundle) + serverNote(data), 'success');
        await reloadConsoleAfterMutation();
    }, 'rollback');
}

async function unplugPluginBundle(bundleId) {
    const text = pluginsT('confirmUnplugHead') + bundleId + pluginsT('confirmUnplugTail');
    if (!window.confirm(text)) return;
    await withPluginBusy(async () => {
        const data = await runPluginRequest('DELETE', '/api/plugins/bundles/' + encodeURIComponent(bundleId));
        notify(pluginsT('unplugDone') + serverNote(data), 'success');
        await reloadConsoleAfterMutation();
    }, 'unplug');
}

async function setPluginUnitEnabled(kind, name, enabled) {
    await withPluginBusy(async () => {
        const data = await runPluginRequest('POST',
            '/api/plugins/units/' + encodeURIComponent(kind) + '/' + encodeURIComponent(name) + '/enabled',
            { enabled: enabled });
        // A plugin switch answers with the capability set the running binary proved it provides.
        // Saying how many landed is the difference between "the switch moved" and "this pack can
        // now be called", and the second one is what the operator clicked for.
        let caps = '';
        if (Array.isArray(data.plugin_capabilities) && data.plugin_capabilities.length) {
            caps = ' · ' + pluginsT('switchCapabilitiesPrefix') + data.plugin_capabilities.length +
                pluginsT('switchCapabilitiesSuffix');
        }
        notify(pluginsT('switchDone') + serverNote(data) + caps, 'success');
        await reloadConsoleAfterMutation();
    }, 'switch');
}

// The response body states in words whenever a mutation did not fully reach the live layer: a tool
// layer that could not rebuild, a server declaration with no manager wired, a switch that will not
// survive a restart, an install that could not be recorded, a snapshot that could not be written.
// Swallowing those would make "done" read as "in effect" - the exact lie the served flag exists to
// prevent.
function serverNote(data) {
    if (!data) return '';
    const parts = [data.tool_layer_error, data.mcp_message, data.plugin_message, data.switch_message,
        data.install_message, data.snapshot_error]
        .filter(text => typeof text === 'string' && text.trim());
    return parts.length ? ' · ' + parts.join(' · ') : '';
}

async function withPluginBusy(fn, label) {
    if (pluginConsoleBusy) return;
    pluginConsoleBusy = true;
    try {
        await fn();
    } catch (err) {
        if (!isPluginConsoleActive()) return;
        const listEl = document.getElementById('plugin-console');
        if (listEl) {
            listEl.insertAdjacentHTML('afterbegin',
                '<div class="plugin-error">' + escapeHtml(pluginsT(label + 'Failed')) + ': ' +
                escapeHtml(err.message) + '</div>');
        }
    } finally {
        pluginConsoleBusy = false;
    }
}

window.loadPluginConsole = loadPluginConsole;
window.renderPluginConsole = renderPluginConsole;
window.installPluginBundle = installPluginBundle;
window.unplugPluginBundle = unplugPluginBundle;
window.setPluginUnitEnabled = setPluginUnitEnabled;
window.rollbackPluginBundle = rollbackPluginBundle;
window.applyPluginFilter = applyPluginFilter;
window.applyPluginKindFilter = applyPluginKindFilter;
window.togglePluginUnitChoice = togglePluginUnitChoice;
window.selectAllPluginUnits = selectAllPluginUnits;
window.removePluginUnit = removePluginUnit;
window.switchPluginTab = switchPluginTab;
