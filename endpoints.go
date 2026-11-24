package nodeadmin

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
)

// endpointRouter is nodeadmin's shared-listener adapter implementing
// the nodes.HTTPRouter seam: nodes configured with listener "main"
// (httpTrigger, submitIn) mount their routes here and serve on the
// admin handler — the Node-RED model where http-in declares a path
// and serves it on the admin port. Routes serve verbatim but are
// dispatched inside admin.ServeHTTP, AFTER the auth middleware, so an
// auth-protected admin protects node endpoints too (documented
// feature; no per-route opt-out in v1).
//
// Dispatch is MANUAL, not a net/http ServeMux: the router keeps a
// route table of parsed patterns and matches each incoming request
// itself (one wildcard catch-all on the listener side, literal-segment
// dispatch here). ServeMux panics when two registered patterns have
// overlapping wildcards of equal specificity — e.g. "/a/{x}/c" vs
// "/a/b/{y}" — and copy-on-write rebuilds still surface that panic.
// The manual matcher follows the same specificity rule (more literal
// segments wins) but reports ambiguous registrations as an error from
// Handle instead of ever panicking.
//
// Pattern syntax is the Go 1.22 ServeMux subset the nodes emit:
// optional "METHOD " prefix, rooted path, "{name}" single-segment
// wildcards (produced by nodes.MuxPattern from Node-RED ":name"
// segments). Multi-segment "{name...}" wildcards and "{$}" are not
// supported — no node emits them.
type endpointRouter struct {
	mu     sync.RWMutex
	routes map[string]*routeEntry // keyed by the full registered pattern
}

// routeSegment is one path segment of a parsed route pattern: either
// a literal to match exactly, or a "{name}" wildcard that captures a
// single segment (Populate PathValue for the node's handler).
type routeSegment struct {
	literal  string // exact-match text ("" when wildcard)
	wildcard string // capture name ("" when literal)
}

// routeEntry is a registered node route parsed for manual dispatch.
type routeEntry struct {
	method  string         // "" = any method
	segs    []routeSegment // path split on "/" (leading empty seg dropped)
	handler http.Handler
}

func newEndpointRouter() *endpointRouter {
	return &endpointRouter{routes: make(map[string]*routeEntry)}
}

// reservedEndpointPrefixes are the path prefixes a shared endpoint may
// not claim: admin-owned subtrees a flow could otherwise shadow or
// spoof. The check is a plain prefix match ("/editorial" is refused
// too — erring safe; document rather than carve exceptions).
var reservedEndpointPrefixes = []string{
	"/api",         // admin REST API (/api/*)
	"/editor",      // flow editor surface
	"/dashboard",   // dashboard hub — runtime bridge + future pages
	"/css",         // embedded static assets
	"/js",          //
	"/favicon.ico", //
}

// splitPattern decomposes a route pattern into method + path.
func splitPattern(pattern string) (method, path string) {
	p := pattern
	if i := strings.IndexByte(p, ' '); i >= 0 {
		method = p[:i]
		p = p[i+1:]
	}
	return method, p
}

// parsePattern validates pattern and decomposes it into a routeEntry
// shape (method + segments). Segments containing "{" must be exactly
// "{name}" — embedded braces or multi-segment "{name...}" wildcards
// are rejected.
func parsePattern(pattern string) (method string, segs []routeSegment, err error) {
	method, path := splitPattern(pattern)
	if !strings.HasPrefix(path, "/") {
		return "", nil, fmt.Errorf("route %q is not a rooted path pattern", pattern)
	}
	for _, seg := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if !strings.ContainsAny(seg, "{}") {
			segs = append(segs, routeSegment{literal: seg})
			continue
		}
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") && len(seg) > 2 {
			name := seg[1 : len(seg)-1]
			if strings.ContainsAny(name, "{}") || strings.HasSuffix(name, "...") {
				return "", nil, fmt.Errorf("route %q: multi-segment or malformed wildcard %q is not supported", pattern, seg)
			}
			segs = append(segs, routeSegment{wildcard: name})
			continue
		}
		return "", nil, fmt.Errorf("route %q: malformed segment %q", pattern, seg)
	}
	return method, segs, nil
}

// checkEndpointPattern rejects routes whose path claims "/" (the admin
// catch-all) or a reserved admin prefix.
func checkEndpointPattern(pattern string) error {
	_, path := splitPattern(pattern)
	if path == "/" {
		return fmt.Errorf("route %q is reserved: \"/\" is the admin UI", pattern)
	}
	for _, p := range reservedEndpointPrefixes {
		if path == p || strings.HasPrefix(path, p+"/") || strings.HasPrefix(path, p+"{") {
			return fmt.Errorf("route %q is under the reserved admin prefix %q", pattern, p)
		}
	}
	return nil
}

// methodsOverlap reports whether two pattern methods can match the
// same request (equal, or either empty = any method).
func methodsOverlap(a, b string) bool {
	return a == "" || b == "" || a == b
}

// compareSpecificity compares two parsed same-length patterns.
// Returns (overlap, winner): overlap reports whether a request path
// could match both; winner is 1 when a is strictly more specific
// (literal where b is wildcard), -1 when b is, 0 when neither is
// (ambiguous — the case ServeMux panics on).
func compareSpecificity(a, b []routeSegment) (overlap bool, winner int) {
	if len(a) != len(b) {
		return false, 0
	}
	aWins, bWins := 0, 0
	for i := range a {
		switch {
		case a[i].wildcard == "" && b[i].wildcard == "":
			if a[i].literal != b[i].literal {
				return false, 0 // disjoint literals can never match the same path
			}
		case a[i].wildcard != "" && b[i].wildcard == "":
			bWins++
		case a[i].wildcard == "" && b[i].wildcard != "":
			aWins++
		}
	}
	switch {
	case aWins > 0 && bWins > 0:
		return true, 0 // each is literal where the other is wild: ambiguous
	case aWins > 0:
		return true, 1
	case bWins > 0:
		return true, -1
	}
	return true, 0 // identical shape (wildcards may differ in name only)
}

// Handle mounts handler under pattern — a conflicting registration
// (ambiguous overlap, or the exact pattern twice) is returned as an
// error so the engine fails the second registrant's deploy.
func (r *endpointRouter) Handle(pattern string, h http.Handler) error {
	if err := checkEndpointPattern(pattern); err != nil {
		return err
	}
	method, segs, err := parsePattern(pattern)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.routes[pattern]; exists {
		return fmt.Errorf("route %q is already mounted", pattern)
	}
	for p, e := range r.routes {
		if !methodsOverlap(method, e.method) {
			continue
		}
		overlap, winner := compareSpecificity(segs, e.segs)
		if overlap && winner == 0 {
			return fmt.Errorf("route %q conflicts with the already-mounted route %q (ambiguous wildcard overlap)", pattern, p)
		}
	}
	r.routes[pattern] = &routeEntry{method: method, segs: segs, handler: h}
	return nil
}

// Unregister removes the route mounted for pattern (no-op when absent).
func (r *endpointRouter) Unregister(pattern string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.routes, pattern)
}

// match reports whether e matches the request, returning the captured
// wildcard values on success.
func (e *routeEntry) match(path string) (map[string]string, bool) {
	segs := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(segs) != len(e.segs) {
		return nil, false
	}
	var values map[string]string
	for i, seg := range e.segs {
		if seg.wildcard != "" {
			if segs[i] == "" {
				return nil, false // a wildcard captures a non-empty segment
			}
			if values == nil {
				values = make(map[string]string)
			}
			values[seg.wildcard] = segs[i]
			continue
		}
		if seg.literal != segs[i] {
			return nil, false
		}
	}
	return values, true
}

// literals counts the literal (non-wildcard) segments — the
// specificity score used for best-match dispatch.
func (e *routeEntry) literals() int {
	n := 0
	for _, s := range e.segs {
		if s.wildcard == "" {
			n++
		}
	}
	return n
}

// serve reports whether a mounted route claims the request and, if so,
// serves it — wildcard values land on r.PathValue exactly as ServeMux
// would set them. When the path matches a mounted route but the
// method does not, it answers 405 (ServeMux parity).
func (r *endpointRouter) serve(w http.ResponseWriter, req *http.Request) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	path := req.URL.Path
	var best *routeEntry
	var bestValues map[string]string
	bestLits := -1
	var allowed []string

	for _, e := range r.routes {
		values, ok := e.match(path)
		if !ok {
			continue
		}
		if e.method != "" && e.method != req.Method {
			allowed = append(allowed, e.method)
			continue
		}
		if lits := e.literals(); lits > bestLits {
			best, bestValues, bestLits = e, values, lits
		}
	}

	if best == nil {
		if len(allowed) > 0 {
			sort.Strings(allowed)
			w.Header().Set("Allow", strings.Join(allowed, ", "))
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return true
		}
		return false
	}
	for name, v := range bestValues {
		req.SetPathValue(name, v)
	}
	best.handler.ServeHTTP(w, req)
	return true
}
