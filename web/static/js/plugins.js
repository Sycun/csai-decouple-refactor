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
    parts.push(renderPluginFilterBar(catalog));

    const sections = pluginSectionsHtml();
    // The two list sections carry ids so a filter keystroke can re-render just their content:
    // replacing the whole console would rebuild the input element and drop focus mid-word.
    parts.push('<section class="plugin-section" id="plugin-section-available"><h3 class="plugin-section-title">' +
        escapeHtml(pluginsT('availableTitle')) + '</h3>' + sections.available + '</section>');
    parts.push('<section class="plugin-section" id="plugin-section-installed"><h3 class="plugin-section-title">' +
        escapeHtml(pluginsT('installedTitle')) + '</h3>' + sections.installed + '</section>');

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
    const installables = visibleCatalog.filter(b => {
        const installed = installedById.get(b.id);
        return !installed || packVersionDiffers(b, installed);
    });
    const availableHtml = installables.length
        ? installables.map(b => renderPluginAvailableCard(b, installedById.get(b.id))).join('')
        : '<div class="empty-state">' + escapeHtml(pluginConsoleFilter ? pluginsT('filterEmpty') : pluginsT('nothingAvailable')) + '</div>';

    const visibleInstalled = (state.bundles || []).filter(pluginMatchesFilter);
    const installedHtml = visibleInstalled.length
        ? visibleInstalled.map(b => renderPluginInstalledCard(b, catalogById.get(b.id))).join('')
        : '<div class="empty-state">' + escapeHtml(pluginConsoleFilter ? pluginsT('filterEmpty') : pluginsT('nothingInstalled')) + '</div>';

    return { available: availableHtml, installed: installedHtml };
}

function pluginMatchesFilter(pack) {
    if (!pluginConsoleFilter) return true;
    if (!pack) return false;
    const hay = [
        pack.id, pack.name, pack.description, pack.author,
        (pack.categories || []).join(' '),
    ].filter(Boolean).join(' ').toLowerCase();
    return hay.indexOf(pluginConsoleFilter.toLowerCase()) >= 0;
}

// applyPluginFilter is the input handler: it re-renders only the two list sections, so the text
// field keeps its focus and the operator can keep typing.
function applyPluginFilter(query) {
    pluginConsoleFilter = String(query == null ? '' : query).trim();
    const listEl = document.getElementById('plugin-console');
    if (!listEl || !pluginConsoleState) return;
    const sections = pluginSectionsHtml();
    const available = document.getElementById('plugin-section-available');
    const installed = document.getElementById('plugin-section-installed');
    if (available) {
        available.innerHTML = '<h3 class="plugin-section-title">' + escapeHtml(pluginsT('availableTitle')) +
            '</h3>' + sections.available;
    }
    if (installed) {
        installed.innerHTML = '<h3 class="plugin-section-title">' + escapeHtml(pluginsT('installedTitle')) +
            '</h3>' + sections.installed;
    }
    if (typeof window.applyRBACToUI === 'function') {
        window.applyRBACToUI(listEl);
    }
}

function renderPluginFilterBar(catalog) {
    const categories = [];
    catalog.forEach(b => {
        (b && b.categories ? b.categories : []).forEach(c => {
            if (categories.indexOf(c) < 0) categories.push(c);
        });
    });
    const chips = categories.length
        ? '<div class="plugin-filter-categories">' + categories.map(c =>
            '<button type="button" class="plugin-chip plugin-filter-chip" onclick="applyPluginFilter(' +
            escapeAttr(JSON.stringify(c)) + ')">' + escapeHtml(c) + '</button>').join('') + '</div>'
        : '';
    return '<div class="plugin-filter">' +
        '<input id="plugin-filter-input" type="text" class="plugin-filter-input" placeholder="' +
        escapeAttr(pluginsT('filterPlaceholder')) + '" value="' + escapeAttr(pluginConsoleFilter) +
        '" oninput="applyPluginFilter(this.value)">' +
        chips + '</div>';
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

function renderPluginAvailableCard(pack, installed) {
    const units = pack.units || [];
    const kinds = {};
    units.forEach(u => { kinds[u.kind] = (kinds[u.kind] || 0) + 1; });
    const kindText = Object.keys(kinds).sort().map(k => unitKindLabel(k) + ' × ' + kinds[k]).join('、');
    const upgrade = packVersionDiffers(pack, installed);
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
    return '<article class="plugin-card' + (pack.error ? ' plugin-card-broken' : '') + '">' +
        '<header class="plugin-card-head">' +
        '<h4>' + escapeHtml(pack.name || pack.id) + '</h4>' +
        '<span class="plugin-card-meta">' + versionLine + '</span>' +
        '</header>' +
        (pack.description ? '<p class="plugin-card-desc">' + escapeHtml(pack.description) + '</p>' : '') +
        renderCatalogueMeta(pack) +
        (kindText ? '<p class="plugin-card-units">' + escapeHtml(kindText) + '</p>' : '') +
        renderPreviewBadges(pack.preview) +
        upgradeLine + diffLine +
        broken +
        (pack.error ? '' :
            '<div class="plugin-card-actions">' +
            '<button class="btn-primary btn-sm" data-require-permission="plugins:install" ' +
            'onclick="installPluginBundle(' + installArg + ')">' +
            escapeHtml(upgrade ? pluginsT('upgrade') : pluginsT('install')) + '</button></div>') +
        '</article>';
}

function renderPluginInstalledCard(pack, catalogEntry) {
    const units = pack.units || [];
    const idArg = escapeAttr(JSON.stringify(pack.id));
    const upgradable = packVersionDiffers(catalogEntry, pack);
    const upgradeChip = upgradable
        ? '<p class="plugin-card-meta-line">' + escapeHtml(
            pluginsT('upgradeAvailablePrefix') + catalogEntry.version + pluginsT('upgradeAvailableSuffix')) + '</p>'
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
    return '<article class="plugin-card plugin-card-installed">' +
        '<header class="plugin-card-head">' +
        '<h4>' + escapeHtml(pack.name || pack.id) + '</h4>' +
        '<span class="plugin-card-meta">' + escapeHtml(pack.id) +
        (pack.version ? ' · v' + escapeHtml(pack.version) : '') + '</span>' +
        '</header>' +
        (pack.description ? '<p class="plugin-card-desc">' + escapeHtml(pack.description) + '</p>' : '') +
        renderCatalogueMeta(pack) +
        upgradeChip +
        '<table class="plugin-table"><thead><tr>' +
        '<th>' + escapeHtml(pluginsT('colKind')) + '</th>' +
        '<th>' + escapeHtml(pluginsT('colName')) + '</th>' +
        '<th>' + escapeHtml(pluginsT('colServed')) + '</th>' +
        '<th>' + escapeHtml(pluginsT('colEnabled')) + '</th>' +
        '</tr></thead><tbody>' +
        units.map(u => '<tr>' + unitCells(u) + '<td>' + unitSwitch(u) + '</td></tr>').join('') +
        '</tbody></table>' +
        rollbackHtml +
        '<div class="plugin-card-actions">' +
        '<button class="btn-secondary btn-sm" data-require-permission="plugins:install" ' +
        'onclick="unplugPluginBundle(' + idArg + ')">' +
        escapeHtml(pluginsT('unplug')) + '</button></div>' +
        '</article>';
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
function installConfirmText(pack, installed) {
    const upgrade = packVersionDiffers(pack, installed);
    const lines = [];
    lines.push((upgrade ? pluginsT('confirmUpgradeHead') : pluginsT('confirmInstallHead')) +
        (pack.name || pack.id) + pluginsT('confirmInstallTail'));
    if (upgrade) {
        lines.push(pluginsT('confirmUpgradePrefix') + 'v' + installed.version + ' → v' + pack.version);
        const text = diffSummaryText(unitDiff(installed.units, pack.units));
        if (text) lines.push(pluginsT('differencesLabel') + text);
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
    const units = (bundle && bundle.units) || [];
    if (!units.length) return '';
    const kinds = {};
    units.forEach(u => { kinds[u.kind] = (kinds[u.kind] || 0) + 1; });
    const text = Object.keys(kinds).sort().map(k => unitKindLabel(k) + ' × ' + kinds[k]).join('、');
    return text ? ' · ' + text : '';
}

async function installPluginBundle(bundleId) {
    const catalog = (pluginConsoleCatalog && pluginConsoleCatalog.bundles) || [];
    const pack = catalog.filter(b => b.id === bundleId)[0];
    const installed = pluginConsoleState &&
        (pluginConsoleState.bundles || []).filter(b => b.id === bundleId)[0];
    const upgrade = packVersionDiffers(pack, installed);
    // Installing always asks first. Without catalogue data there is no preview to show, but
    // skipping the dialog would make "no information" look like "nothing to tell" - the same lie
    // the served flag exists to prevent.
    const text = pack
        ? installConfirmText(pack, installed)
        : pluginsT('confirmInstallHead') + bundleId + pluginsT('confirmInstallTail');
    if (!window.confirm(text)) return;
    await withPluginBusy(async () => {
        const data = await runPluginRequest('POST', '/api/plugins/install', { bundle: bundleId });
        const base = upgrade
            ? pluginsT('upgradeDone')
            : (data.tools_rebuilt ? pluginsT('installWithTools') : pluginsT('installPlain'));
        notify(`${base} ${data.bundle && data.bundle.id ? data.bundle.id : bundleId}` +
            `${unitSummaryText(data.bundle)}${serverNote(data)}`, 'success');
        await reloadConsoleAfterMutation();
    }, upgrade ? 'upgrade' : 'install');
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
