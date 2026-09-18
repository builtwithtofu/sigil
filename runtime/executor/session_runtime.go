package executor

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"

	"github.com/builtwithtofu/sigil/core/decorator"
	"github.com/builtwithtofu/sigil/core/planfmt"
)

type sessionFactory func(transportID string) (decorator.Session, error)

type AuthFingerprint struct {
	User          string
	Method        string
	HostKeyPolicy string
	PasswordHash  string
}

func (af AuthFingerprint) Hash() string {
	sum := sha256.Sum256([]byte(af.User + ":" + af.Method + ":" + af.HostKeyPolicy + ":" + af.PasswordHash))
	return hex.EncodeToString(sum[:8])
}

type sessionRuntime struct {
	mu         sync.Mutex
	sessions   map[string]decorator.Session
	direct     map[string]decorator.Session
	pooled     map[string]decorator.Session
	transports map[string]planfmt.Transport
	factory    sessionFactory
}

func newSessionRuntime(factory sessionFactory) *sessionRuntime {
	if factory == nil {
		factory = defaultSessionFactory
	}

	return &sessionRuntime{
		sessions:   make(map[string]decorator.Session),
		direct:     make(map[string]decorator.Session),
		pooled:     make(map[string]decorator.Session),
		transports: make(map[string]planfmt.Transport),
		factory:    factory,
	}
}

func (r *sessionRuntime) registerPlanTransports(transports []planfmt.Transport) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.transports = make(map[string]planfmt.Transport, len(transports))
	for _, transport := range transports {
		if transport.ID == "" {
			continue
		}
		r.transports[normalizedTransportID(transport.ID)] = transport
	}
}

func defaultSessionFactory(transportID string) (decorator.Session, error) {
	// Factory is only called for local transports (either literal "local" or derived IDs).
	// Create a LocalSession regardless of the specific transport ID format.
	return decorator.NewLocalSession(), nil
}

func normalizedTransportID(transportID string) string {
	if transportID == "" {
		return "local"
	}
	return transportID
}

func (r *sessionRuntime) SessionFor(transportID string) (decorator.Session, error) {
	key := normalizedTransportID(transportID)

	r.mu.Lock()
	if session, ok := r.sessions[key]; ok {
		r.mu.Unlock()
		return session, nil
	}
	r.mu.Unlock()

	session, pooled, err := r.createSession(key)
	if err != nil {
		return nil, fmt.Errorf("failed to create session for transport %q: %w", key, err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.sessions[key]; ok {
		return existing, nil
	}
	r.sessions[key] = session
	if !pooled {
		r.direct[key] = session
	}
	return session, nil
}

func (r *sessionRuntime) createSession(transportID string) (decorator.Session, bool, error) {
	transport, ok := r.lookupTransport(transportID)
	if !ok {
		if transportID == "local" {
			session, err := r.factory(transportID)
			return session, false, err
		}

		if existing, found := r.lookupSessionByIdentity(transportID); found {
			return existing, true, nil
		}

		return nil, false, fmt.Errorf("unknown transport %q: transport not registered", transportID)
	}

	if normalizedTransportID(transport.Decorator) == "local" {
		session, err := r.factory(transportID)
		if err == nil {
			for _, arg := range transport.Args {
				if arg.Key == "cwd" && arg.Val.Kind == planfmt.ValueString {
					session = session.WithWorkdir(arg.Val.Str)
				}
			}
		}
		return session, false, err
	}

	transportDecorator, err := lookupTransportDecoratorForSession(transport.Decorator)
	if err != nil {
		return nil, false, err
	}

	name := strings.TrimPrefix(transport.Decorator, "@")
	if !isTransportSessionMaterialized(name) {
		session, err := r.factory(transportID)
		return session, false, err
	}

	parentID := normalizedTransportID(transport.ParentID)
	if parentID == "" {
		parentID = "local"
	}
	parentSession, err := r.SessionFor(parentID)
	if err != nil {
		return nil, false, err
	}

	poolKey := transportPoolKey(name, parentSession, planArgsToMap(transport.Args))

	r.mu.Lock()
	if pooled, ok := r.pooled[poolKey]; ok {
		r.mu.Unlock()
		return &transportScopedSession{id: transportID, session: pooled}, true, nil
	}
	r.mu.Unlock()

	openedSession, err := transportDecorator.Open(parentSession, planArgsToMap(transport.Args))
	if err != nil {
		return nil, false, fmt.Errorf("open transport %q: %w", transport.Decorator, err)
	}

	r.mu.Lock()
	if pooled, ok := r.pooled[poolKey]; ok {
		r.mu.Unlock()
		_ = openedSession.Close()
		return &transportScopedSession{id: transportID, session: pooled}, true, nil
	}
	r.pooled[poolKey] = openedSession
	r.mu.Unlock()

	return &transportScopedSession{id: transportID, session: openedSession}, true, nil
}

func transportPoolKey(decoratorName string, parentSession decorator.Session, params map[string]any) string {
	return fmt.Sprintf("%s:%s", decoratorName, SessionPoolKey(parentSession, params))
}

func SessionPoolKey(parentSession decorator.Session, params map[string]any) string {
	parentContext := "root"
	if parentSession != nil {
		if parentID := strings.TrimSpace(parentSession.ID()); parentID != "" {
			parentContext = parentID
		}
	}

	authFingerprint := sessionAuthFingerprint(params)
	authHash := authFingerprint.Hash()

	keys := make([]string, 0, len(params))
	for key := range params {
		if isAuthPoolParam(key) {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var buf strings.Builder
	buf.WriteString(parentContext)
	buf.WriteString(":")
	buf.WriteString(authHash)
	buf.WriteString(":")
	for _, key := range keys {
		fmt.Fprintf(&buf, "%s=%v;", key, params[key])
	}

	sum := sha256.Sum256([]byte(buf.String()))
	return fmt.Sprintf("%s:%s:%s", parentContext, authHash, hex.EncodeToString(sum[:8]))
}

func sessionAuthFingerprint(params map[string]any) AuthFingerprint {
	user := "default"
	if rawUser, ok := params["user"].(string); ok {
		if trimmed := strings.TrimSpace(rawUser); trimmed != "" {
			user = trimmed
		}
	}

	method := "agent"
	if hasNonEmptyParam(params, "password") {
		method = "password"
	}

	passwordHash := ""
	if rawPassword, ok := params["password"].(string); ok {
		if trimmedPassword := strings.TrimSpace(rawPassword); trimmedPassword != "" {
			passwordHash = hashString(trimmedPassword)
		}
	}

	if rawKey, ok := params["key"]; ok {
		method = authMethodFromKey(rawKey)
	}

	hostKeyPolicy := "strict"
	if strictHostKey, ok := params["strict_host_key"].(bool); ok && !strictHostKey {
		hostKeyPolicy = "insecure"
	} else if knownHostsPath, ok := params["known_hosts_path"].(string); ok {
		trimmedPath := strings.TrimSpace(knownHostsPath)
		if trimmedPath != "" {
			hostKeyPolicy = "strict:" + hashString(trimmedPath)
		}
	}

	return AuthFingerprint{
		User:          user,
		Method:        method,
		HostKeyPolicy: hostKeyPolicy,
		PasswordHash:  passwordHash,
	}
}

func hasNonEmptyParam(params map[string]any, key string) bool {
	raw, ok := params[key]
	if !ok || raw == nil {
		return false
	}

	if value, ok := raw.(string); ok {
		return strings.TrimSpace(value) != ""
	}

	return true
}

func authMethodFromKey(rawKey any) string {
	switch key := rawKey.(type) {
	case string:
		trimmed := strings.TrimSpace(key)
		if trimmed == "" {
			// Whitespace-only key is invalid; use distinct fingerprint to prevent
			// pool collision with real agent auth. This ensures the validation
			// error from dialSSHClient surfaces instead of silently reusing pooled session.
			return "key:blank"
		}
		return "keyfile:" + hashString(trimmed)
	default:
		return "key"
	}
}

func hashString(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:8])
}

func isAuthPoolParam(key string) bool {
	switch key {
	case "user", "key", "password", "strict_host_key", "known_hosts_path":
		return true
	default:
		return false
	}
}

func (r *sessionRuntime) lookupTransport(transportID string) (planfmt.Transport, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	transport, ok := r.transports[normalizedTransportID(transportID)]
	return transport, ok
}

func (r *sessionRuntime) lookupSessionByIdentity(sessionID string) (decorator.Session, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, session := range r.sessions {
		if normalizedTransportID(session.ID()) == normalizedTransportID(sessionID) {
			return session, true
		}
	}

	for _, session := range r.pooled {
		if normalizedTransportID(session.ID()) == normalizedTransportID(sessionID) {
			return session, true
		}
	}

	return nil, false
}

func isTransportSessionMaterialized(name string) bool {
	transport, ok, _ := decorator.Global().GetTransport(name)
	if !ok {
		return false
	}

	return transport.MaterializeSession()
}

func lookupTransportDecoratorForSession(decoratorName string) (decorator.Transport, error) {
	name := strings.TrimPrefix(decoratorName, "@")
	transport, ok, reason := decorator.Global().GetTransport(name)
	if !ok {
		if reason != "" {
			return nil, fmt.Errorf("decorator %q is not a transport", decoratorName)
		}
		return nil, fmt.Errorf("unknown transport decorator %q", decoratorName)
	}

	return transport, nil
}

func (r *sessionRuntime) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	closers := r.closeTargetsByTransportIDLocked()
	for _, transportID := range r.postorderTransportIDsLocked() {
		session, ok := closers[transportID]
		if !ok {
			continue
		}
		if err := session.Close(); err != nil {
			log.Printf("session runtime: close transport %q: %v", transportID, err)
		}
	}

	r.sessions = make(map[string]decorator.Session)
	r.direct = make(map[string]decorator.Session)
	r.pooled = make(map[string]decorator.Session)
}

func (r *sessionRuntime) closeTargetsByTransportIDLocked() map[string]decorator.Session {
	closers := make(map[string]decorator.Session)
	for transportID, session := range r.direct {
		closers[transportID] = session
	}

	closed := make(map[decorator.Session]struct{})
	for transportID, session := range closers {
		closed[session] = struct{}{}
		closers[transportID] = session
	}

	for transportID, wrapped := range r.sessions {
		scoped, ok := wrapped.(*transportScopedSession)
		if !ok || scoped == nil || scoped.session == nil {
			continue
		}
		if _, alreadyAdded := closed[scoped.session]; alreadyAdded {
			continue
		}
		closers[transportID] = scoped.session
		closed[scoped.session] = struct{}{}
	}

	return closers
}

func (r *sessionRuntime) postorderTransportIDsLocked() []string {
	allIDs := make(map[string]struct{})
	children := make(map[string][]string)

	for transportID, transport := range r.transports {
		id := normalizedTransportID(transportID)
		allIDs[id] = struct{}{}
		parentID := normalizedTransportID(transport.ParentID)
		if parentID == "" {
			parentID = "local"
		}
		if parentID != id {
			children[parentID] = append(children[parentID], id)
			allIDs[parentID] = struct{}{}
		}
	}

	for transportID := range r.direct {
		allIDs[transportID] = struct{}{}
	}
	for transportID := range r.sessions {
		allIDs[transportID] = struct{}{}
	}

	for parentID := range children {
		sort.Strings(children[parentID])
	}

	orderedIDs := make([]string, 0, len(allIDs))
	for id := range allIDs {
		orderedIDs = append(orderedIDs, id)
	}
	sort.Strings(orderedIDs)

	visited := make(map[string]struct{}, len(orderedIDs))
	postorder := make([]string, 0, len(orderedIDs))
	var visit func(id string)
	visit = func(id string) {
		if _, seen := visited[id]; seen {
			return
		}
		visited[id] = struct{}{}
		for _, childID := range children[id] {
			visit(childID)
		}
		postorder = append(postorder, id)
	}

	for _, id := range orderedIDs {
		visit(id)
	}

	return postorder
}
