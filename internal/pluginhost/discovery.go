package pluginhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Capability discovery is the host asking the plugin what it provides.
//
// The ABI has always carried capabilities/list, but nothing called it: the only way a plugin
// capability could exist was for someone to hand-write a recipe declaring it. That reverses the
// point of a plug-in - the code and its entry points live together, and the platform learns them
// from the running thing.
//
// What discovery returns is deliberately *only* identifiers. Level, permission, grants and
// human-readable description are not a plugin's to claim: they are reviewed content that ships
// beside the binary in the pack's manifest, and installation refuses any disagreement between the
// two (see internal/plugin). A plugin that could describe itself could describe its way into
// `destructive`.
//
// Ids are also namespace-checked here rather than later. Service.Invoke routes a capability to an
// instance by the first segment of its id, so a plugin allowed to advertise "other-vendor.tool"
// would be handing itself a call into a different publisher's process - a different trust domain,
// possibly a wider grant set. Only ids inside the instance's own publisher namespace are accepted.

var discoverableCapabilityID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*(\.[a-z0-9][a-z0-9_-]*)+$`)

// Capabilities asks this plugin what it provides and returns the validated set, sorted.
//
// An empty list is a valid answer (a plugin that invokes nothing), and is reported as such - it
// is the pack's cross-check that turns "the binary does not actually provide what the manifest
// says" into a refusal.
func (i *Instance) Capabilities(ctx context.Context, timeout time.Duration) (ids []string, err error) {
	// A broken plugin costs this call an error, and nothing else: same contract as Invoke, so a
	// discovery sweep over several domains cannot take the host process down. The error must not
	// be swallowed into an empty list - empty means "this plugin provides nothing", and a pack
	// would read that as permission to register no capabilities.
	defer func() {
		if recovered := recover(); recovered != nil {
			ids = nil
			err = fmt.Errorf("pluginhost: recovered while listing capabilities: %v", recovered)
		}
	}()
	if timeout <= 0 {
		timeout = i.cfg.CallTimeout
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if err := i.ensureStarted(callCtx); err != nil {
		return nil, err
	}
	response, err := i.roundTrip(callCtx, MethodListCaps, nil, timeout)
	if err != nil {
		return nil, err
	}
	if response.Error != nil {
		return nil, response.Error
	}
	ids, err = decodeCapabilityList(response.Result, i.cfg.PluginID)
	if err != nil {
		return nil, err
	}
	i.markUsed()
	return ids, nil
}

// decodeCapabilityList accepts exactly the ABI shape - an array of strings - and rejects
// everything else, including a well-formed array whose names are not usable.
//
// Decoding into []string rather than []json.RawMessage is the strict half: an object-form answer
// ({"id": "x"}) fails here with a message that says what the ABI expects, instead of silently
// yielding a list nobody can read.
func decodeCapabilityList(raw json.RawMessage, pluginID string) ([]string, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("pluginhost: %s returned no result", MethodListCaps)
	}
	var reported []string
	if err := json.Unmarshal(raw, &reported); err != nil {
		return nil, fmt.Errorf("pluginhost: %s must answer with an array of capability ids: %w", MethodListCaps, err)
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(reported))
	for _, id := range reported {
		id = strings.TrimSpace(id)
		switch {
		case id == "":
			return nil, fmt.Errorf("pluginhost: %s reported a blank capability id", MethodListCaps)
		case !discoverableCapabilityID.MatchString(id):
			return nil, fmt.Errorf("pluginhost: %s reported %q, which is not a dotted capability id", MethodListCaps, id)
		case !insidePublisher(id, pluginID):
			return nil, fmt.Errorf("pluginhost: %s reported %q outside its own publisher namespace %q", MethodListCaps, id, pluginID)
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

// insidePublisher is the namespace rule. DomainForTrust takes the first dotted segment of a
// capability id as its trust domain, so the publisher prefix of an advertised id has to be this
// instance's own plugin id - otherwise the host would route a call from this plugin into some
// other publisher's process.
func insidePublisher(capabilityID, pluginID string) bool {
	head, _, ok := strings.Cut(capabilityID, ".")
	return ok && head == strings.TrimSpace(pluginID)
}

// ListCapabilities asks one trust domain's plugin what it provides. The instance is created the
// same way Invoke creates it, and lazily started by the same path, so discovery is the only
// reason a pack-declared plugin would ever run a process - which is what makes "install, then
// look" safe.
func (s *Service) ListCapabilities(ctx context.Context, domain string, timeout time.Duration) ([]string, error) {
	if s == nil {
		return nil, ErrNotConfigured
	}
	instance, err := s.instanceFor(domain)
	if err != nil {
		return nil, err
	}
	return instance.Capabilities(ctx, timeout)
}

// instanceFor is the shared get-or-create that Invoke used to inline. Discovery and invocation
// must agree on exactly one instance per trust domain, because two instances would mean two
// grant sets for one publisher.
func (s *Service) instanceFor(domain string) (*Instance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("pluginhost: service is closed")
	}
	if instance, ok := s.instances[domain]; ok {
		return instance, nil
	}
	cfg, exists := s.configs[domain]
	if !exists {
		return nil, fmt.Errorf("%w: trust domain %q is not configured", ErrNotConfigured, domain)
	}
	instance, err := NewInstance(cfg, s.logger)
	if err != nil {
		return nil, err
	}
	s.instances[domain] = instance
	return instance, nil
}
