// One translation lookup for the whole console, carrying the two behaviors the console uses.
//
// Before this file, `_t` was written out in seven scripts - and not as seven copies of one
// function but as three different ones: five files returned whatever i18next answered (or the
// key when i18next had not loaded yet), workflows.js treated "the answer is the key itself" as a
// miss and swallowed exceptions, and roles.js did both of those plus substituted three hard-coded
// Chinese strings. The name was the same, so a page's behavior depended on which script last
// defined it at global scope - and the fallbacks that were genuinely role-page copy sat inside a
// function that looked generic.
//
// Both behaviors are kept, because they are not interchangeable: tOrKey passes a deliberately
// empty translation through as empty (a page that wants "no text" gets no text), while tFallback
// treats an answer equal to the key, or a throw, or an empty string as "not translated" and falls
// back. Pick one at the call site; do not re-implement either here.
(function () {
    window.CSAI = window.CSAI || {};

    // tOrKey is the plain lookup. The typeof guard is the whole point: the dictionaries and
    // i18next load asynchronously, so a script can run before window.t exists, and the page must
    // still render something.
    function tOrKey(key, opts) {
        return typeof window.t === 'function' ? window.t(key, opts) : key;
    }

    // tFallback is the guarded lookup: only an answer that is a non-empty string and differs
    // from the key counts as translated. `copy` is a page's own last-resort wording, passed in by
    // that page so the vocabulary stays where it belongs.
    function tFallback(key, opts, copy) {
        if (typeof window.t === 'function') {
            try {
                const translated = window.t(key, opts);
                if (typeof translated === 'string' && translated && translated !== key) {
                    return translated;
                }
            } catch (e) { /* i18next throwing is a miss, not a crash */ }
        }
        if (copy && Object.prototype.hasOwnProperty.call(copy, key)) return copy[key];
        return key;
    }

    window.CSAI.tOrKey = tOrKey;
    window.CSAI.tFallback = tFallback;
})();
