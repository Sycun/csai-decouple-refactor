# Frontend i18n

[中文](../zh-CN/frontend-i18n.md)

CyberStrikeAI frontend i18n is static and lightweight. Text is organized in JSON files and applied through `data-i18n` attributes plus JavaScript helper functions.

## Files

```text
web/static/i18n/zh-CN.json
web/static/i18n/en-US.json
web/static/js/i18n.js
```

## Key Principles

- Keep keys stable and semantic.
- Update Chinese and English together.
- Do not hardcode new visible text in JS when it should be localized.
- Preserve default HTML text as fallback before JS initialization.

## HTML Usage

```html
<button data-i18n="common.save">保存</button>
```

For attributes, follow the existing `i18n.js` conventions.

## JavaScript Usage

Use the global translation helper where available:

```javascript
const label = t('common.save');
```

When adding dynamic UI, make sure language switching refreshes the text or re-renders the component.

When a page needs a wrapper that tolerates i18next not being ready yet, use the shared one in
`web/static/js/i18n-tag.js` instead of writing another `_t`:

- `CSAI.tOrKey(key, opts)` - returns exactly what i18next answered (including an empty string),
  and the key only when i18next is absent.
- `CSAI.tFallback(key, opts, copy)` - counts an answer as translated only when it is a non-empty
  string different from the key; otherwise it falls back to a copy table the **calling page passes
  in**, then to the key. A throw from i18next counts as a miss.

They are two behaviors, not one, which is why they are two names: `_t` used to be written out in
seven scripts as three different implementations, all at global scope on the same page, so which
one answered depended on script order. Keep a page's own last-resort wording in that page
(`roles.js` has `ROLE_COPY_FALLBACK`); `web/static/js/i18n-tag.test.cjs`, run by `make js-check`,
enforces both rules and ratchets the number of files still carrying an inline
`typeof window.t === 'function'` guard, which may only fall.

## Migration Workflow

1. Add or update UI text.
2. Add keys to `zh-CN.json`.
3. Add matching keys to `en-US.json`.
4. Replace hardcoded text with `data-i18n` or `t()`.
5. Test both languages and browser console.

## Common Pitfalls

- Missing keys only in one language.
- Dynamic text built from hardcoded fragments.
- Button labels too long in English.
- HTML fallback text diverges from JSON text.
- Adding new page text without updating language switch behavior.
