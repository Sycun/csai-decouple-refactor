package handler

import "fmt"

// openAPIPaths merges the per-group path maps into the one the document serves.
//
// Two guards, both worse to lose than the verbosity this removes:
//
//   - Duplicate paths. Splitting the data across five files means two of them can declare the
//     same path, and a plain merge lets whichever ran later silently win. That is a
//     documentation bug nobody notices until an API consumer gets the wrong operationId, so it
//     panics at startup instead.
//   - A group file that stops being compiled in (renamed, build tag, deleted) would otherwise
//     just make the served document shorter. The floor is above what any four groups could
//     produce, so losing one fails loudly rather than quietly dropping 14 endpoints.
//
// It returns a freshly built map per call: enrichSpecWithI18nKeys writes x-i18n-tags into
// every operation, so a shared package-level map would be two concurrent spec requests
// writing the same map. That is also why these are functions rather than vars.
func openAPIPaths() map[string]interface{} {
	out := make(map[string]interface{}, 128)
	for _, group := range []struct {
		name  string
		paths map[string]interface{}
	}{
		{"chat", openAPIPathsChat()},
		{"knowledge", openAPIPathsKnowledge()},
		{"capabilities", openAPIPathsCapabilities()},
		{"mcp", openAPIPathsMCP()},
		{"ops", openAPIPathsOps()},
	} {
		for path, spec := range group.paths {
			if _, dup := out[path]; dup {
				panic(fmt.Sprintf("openapi: path %s declared twice (second in the %s group)", path, group.name))
			}
			out[path] = spec
		}
	}
	if len(out) < 110 {
		panic(fmt.Sprintf("openapi: merged only %d paths; a group file stopped being compiled in", len(out)))
	}
	return out
}
