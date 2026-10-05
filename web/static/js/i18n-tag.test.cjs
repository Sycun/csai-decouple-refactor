// Gate for the one-lookup rule: web/static/js/i18n-tag.js holds the only implementations, and
// every per-file `_t` is a delegation to one of the two behaviors.
//
// `_t` used to be written out in seven files - not as seven copies of one function but as three
// different ones, so what a page did on a missing translation depended on which script last
// defined the name at global scope, and role-page wording lived inside a helper that looked
// generic. Two behaviors are legitimate; a third one appearing by accident is not.
const test = require('node:test');
const assert = require('node:assert');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const JS_DIR = __dirname;
const TemplateDir = () => path.join(__dirname, '..', '..', 'templates');

// The helpers are loaded into a vm context whose `window` is a plain object, exactly as a
// browser gives them one. `withT` then swaps `window.t`, because the functions close over that
// object - assigning to global.window here would be invisible to them.
function loadTagHelper() {
	const src = fs.readFileSync(path.join(JS_DIR, 'i18n-tag.js'), 'utf8');
	const sandbox = { window: {}, console };
	sandbox.globalThis = sandbox;
	vm.createContext(sandbox);
	vm.runInContext(src, sandbox, { filename: 'i18n-tag.js' });
	const api = sandbox.window.CSAI;
	assert.ok(api && typeof api.tOrKey === 'function' && typeof api.tFallback === 'function',
		'i18n-tag.js must define CSAI.tOrKey and CSAI.tFallback');
	return {
		api,
		withT: (impl, run) => {
			sandbox.window.t = impl;
			try {
				run();
			} finally {
				delete sandbox.window.t;
			}
		},
	};
}

function listSources() {
	return fs.readdirSync(JS_DIR)
		.filter((f) => f.endsWith('.js'))
		.map((f) => [f, fs.readFileSync(path.join(JS_DIR, f), 'utf8')]);
}

test('tOrKey hands back whatever i18next answered and only substitutes the key when it is absent', () => {
	const { api, withT } = loadTagHelper();
	assert.strictEqual(api.tOrKey('a.b'), 'a.b', 'no window.t at all');

	const calls = [];
	withT((key, opts) => { calls.push([key, opts]); return '译文'; }, () => {
		assert.strictEqual(api.tOrKey('a.b', { count: 2 }), '译文');
		assert.deepStrictEqual(calls, [['a.b', { count: 2 }]], 'the options must reach i18next');
	});
	// These two are what make tOrKey a different behavior rather than a weaker tFallback: an
	// empty translation is an answer, and so is whatever else i18next was configured to return.
	// A page that wants "no text here" gets no text.
	withT(() => '', () => assert.strictEqual(api.tOrKey('a.b'), ''));
	withT(() => undefined, () => assert.strictEqual(api.tOrKey('a.b'), undefined));
	withT(() => 'a.b', () => assert.strictEqual(api.tOrKey('a.b'), 'a.b'));
});

test('tFallback only accepts an answer that is a real translation', () => {
	const { api, withT } = loadTagHelper();
	const copy = { 'roles.noDescription': '暂无描述' };

	withT(() => 'No description', () => assert.strictEqual(api.tFallback('roles.noDescription', undefined, copy), 'No description'));
	// An answer equal to the key, empty, non-string, or a throw are all misses, and a miss
	// consults the page's own wording before falling back to the key.
	withT(() => 'roles.noDescription', () => assert.strictEqual(api.tFallback('roles.noDescription', undefined, copy), '暂无描述'));
	withT(() => '', () => assert.strictEqual(api.tFallback('roles.noDescription', undefined, copy), '暂无描述'));
	withT(() => ({ nested: 1 }), () => assert.strictEqual(api.tFallback('roles.noDescription', undefined, copy), '暂无描述'));
	withT(() => { throw new Error('i18next not ready'); }, () => assert.strictEqual(api.tFallback('roles.noDescription', undefined, copy), '暂无描述'));
	assert.strictEqual(api.tFallback('roles.noDescription', undefined, copy), '暂无描述', 'no window.t: the page copy still applies');
	assert.strictEqual(api.tFallback('other.key', undefined, copy), 'other.key', 'an unlisted key falls back to the key');
	// hasOwnProperty rather than `copy[key]`: a key called `constructor` must not be answered by
	// Object.prototype through a table that never had it.
	assert.strictEqual(api.tFallback('constructor', undefined, copy), 'constructor');
});

test('no console script implements its own translation lookup', () => {
	const offenders = [];
	let delegating = 0;
	for (const [name, src] of listSources()) {
		if (name === 'i18n-tag.js' || name.startsWith('generated')) continue;
		for (const line of src.split('\n')) {
			if (!/^\s*(?:(?:const|let|var)\s+_t\s*=|function\s+_t\s*\()/.test(line)) continue;
			delegating++;
			// A delegation is checked by content, not by prefix: monitor.js aliases the shared
			// function (`const _t = CSAI.tOrKey;`) while the pages wrap it in a one-line call.
			const delegated = /CSAI\.t(?:OrKey|Fallback)\b/.test(line);
			if (!delegated || /window\.t\b/.test(line)) offenders.push(`${name}: ${line.trim()}`);
		}
	}
	// Seven wrappers plus four bindings in monitor.js. An empty scan would mean the pattern
	// stopped matching, which is how this kind of gate goes green while a copy comes back.
	assert.ok(delegating >= 11, `expected at least 11 _t declarations in the console, found ${delegating}`);
	assert.deepStrictEqual(offenders, [], 'these files still implement a translation lookup');
});

test('the inline lookup guard may only shrink, and it is name-independent', () => {
	// What this round actually closed is the `_t` family - 7 definitions in 3 different shapes.
	// The pattern is bigger than that name: 23 scripts still call `window.t` behind an inline
	// `typeof window.t === 'function'` guard of their own, which is the same decision written out
	// again. That idiom is not unique to a lookup - it is also how these pages check whether i18n
	// is ready at all - so it cannot be a hard zero today. It can be a ratchet: this number may
	// only go down, and because the check is on the residue rather than the name, a duplicate
	// cannot hide behind a novel name the way it could from the test above.
	const INLINE = /typeof window\.t === ['"]function['"]/;
	const baseline = 23;
	const found = [];
	for (const [name, src] of listSources()) {
		if (name === 'i18n-tag.js' || name.startsWith('generated')) continue;
		if (INLINE.test(src)) found.push(name);
	}
	if (found.length > baseline) {
		assert.fail(`${found.length} scripts carry their own inline window.t guard, baseline is ${baseline}: ${found.join(', ')}`);
	}
	if (found.length < baseline) {
		console.log(`inline window.t guards dropped to ${found.length}; tighten the baseline in i18n-tag.test.cjs`);
	}
	assert.ok(found.length > 0, 'the scan found nothing, which means the pattern stopped matching');
});

test('page-specific wording stays in the page that owns it', () => {
	const shared = fs.readFileSync(path.join(JS_DIR, 'i18n-tag.js'), 'utf8');
	// The helper accepts a copy table and must not carry one - the failure mode this guards is
	// someone pasting strings here "just for now", which puts one page's vocabulary behind every
	// page's lookup.
	assert.ok(!/'[a-zA-Z]+\.[a-zA-Z]+':\s*'/.test(shared), 'i18n-tag.js holds a copy table of its own');
	const roles = fs.readFileSync(path.join(JS_DIR, 'roles.js'), 'utf8');
	assert.ok(roles.includes('ROLE_COPY_FALLBACK'), 'roles.js no longer passes its own wording in');
	assert.ok(roles.includes("'默认角色，不额外携带用户提示词，使用默认MCP'"), 'roles.js lost a last-resort string');
});

test('every template that loads a delegating script loads i18n-tag.js first', () => {
	// The console is one template, but api-docs.js belongs to another page, so the order is
	// asserted per template - a script loaded only from api-docs.html would otherwise be checked
	// by nothing.
	const templates = fs.readdirSync(TemplateDir())
		.filter((f) => f.endsWith('.html'))
		.map((f) => [f, fs.readFileSync(path.join(TemplateDir(), f), 'utf8')]);

	const consumers = {};
	for (const [name, src] of listSources()) {
		if (name === 'i18n-tag.js' || name.startsWith('generated')) continue;
		if (!/CSAI\.t(?:OrKey|Fallback)\b/.test(src)) continue;
		consumers[name] = templates.filter(([, html]) => html.includes(`/static/js/${name}`)).map(([f]) => f);
		assert.ok(consumers[name].length > 0, `${name} delegates but no template loads it`);
	}
	assert.ok(Object.keys(consumers).length >= 7, `only ${Object.keys(consumers).length} scripts delegate - the scan is not seeing the console`);

	for (const [template, html] of templates) {
		const tagAt = html.indexOf('/static/js/i18n-tag.js');
		for (const [script, loadedFrom] of Object.entries(consumers)) {
			if (!loadedFrom.includes(template)) continue;
			assert.ok(tagAt >= 0, `${template} loads ${script} but never loads i18n-tag.js`);
			assert.ok(tagAt < html.indexOf(`/static/js/${script}`), `${template} must load i18n-tag.js before ${script}`);
		}
	}
});
