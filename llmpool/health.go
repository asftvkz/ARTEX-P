// Package llmpool implements LLM failover ("轮询"): a Provider decorator that
// walks an ordered chain of LLM profiles and moves to the next one when the
// current one can't serve the request — out of credit, revoked key, rate-limited
// past the SDK's own retries, or down.
//
// The chain is built by the server from llm_profiles (active profile first, then
// by priority), so the whole process shares ONE chain and ONE circuit-breaker
// registry: when a task discovers a profile is out of credit, every other task
// skips it immediately.
package llmpool

import (
	"fmt"
	"strconv"
	"sync"
	"time"
)

// backoff is the cooling-off ladder, indexed by trip count: the 1st trip cools
// for a minute, the 2nd for five, everything after that for half an hour. A
// profile that is merely rate-limited recovers quickly; one that keeps failing
// stops being probed every round.
var backoff = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute}

// State is one profile's circuit-breaker state.
type State struct {
	Fails     int       // consecutive failures; reset by a success
	Trips     int       // total trips, indexes the backoff ladder
	OpenUntil time.Time // zero / past = closed (usable)
	LastError string
	LastAt    time.Time
}

// Open reports whether the breaker is currently open (profile should be skipped).
func (s State) Open() bool { return time.Now().Before(s.OpenUntil) }

// softTripAfter is the DEFAULT number of consecutive TRANSIENT failures (429 /
// 5xx / network) that trip the breaker. Deterministic failures (no credit, bad
// key) trip on the first regardless. Overridable via Registry.SetPolicy.
const softTripAfter = 3

// Registry holds the circuit-breaker state of every profile, keyed by profile id.
// It is process-wide and outlives any single Pool instance, so rebuilding the pool
// (saving an unrelated profile, toggling a setting) never clears what we've learned
// about which backends are broken.
type Registry struct {
	opMu sync.Mutex
	mu   sync.Mutex
	m    map[string]*State

	// persist / forget mirror state to the DB so a cooling-off window survives a
	// restart. Both may be nil (no DB); both are called off the hot path.
	persist func(id string, st State)
	forget  func(id string)

	// softTrip / cooldown are the operator-set overrides (0 = use the built-in
	// default / ladder). They live here rather than being read per failure because
	// Trip runs on the failure path of every request.
	softTrip int
	cooldown time.Duration
}

// SetPolicy overrides the breaker's two knobs. softTrip: how many consecutive
// transient failures trip it (0 = default softTripAfter; negative = transient
// failures never trip it, leaving only the deterministic ones). cooldown: a fixed
// cooling-off window (0 = the 1min/5min/30min ladder). Safe to call at any time.
func (r *Registry) SetPolicy(softTrip int, cooldown time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.softTrip, r.cooldown = softTrip, cooldown
}

// tripAfter is the effective consecutive-transient-failure threshold. Callers
// hold r.mu. A negative override yields 0, which Trip reads as "never soft-trip".
func (r *Registry) tripAfter() int {
	if r.softTrip == 0 {
		return softTripAfter
	}
	return max(r.softTrip, 0)
}

// coolFor is the cooling-off window for the trips-th trip (1-based): the fixed
// override when set, else the ladder (last rung repeats). Callers hold r.mu.
func (r *Registry) coolFor(trips int) time.Duration {
	if r.cooldown > 0 {
		return r.cooldown
	}
	if trips-1 < len(backoff) {
		return backoff[trips-1]
	}
	return backoff[len(backoff)-1]
}

// NewRegistry builds an empty registry. persist/forget may be nil.
func NewRegistry(persist func(id int64, st State), forget func(id int64)) *Registry {
	var p func(string, State)
	var f func(string)
	if persist != nil {
		p = func(key string, st State) { id, _ := strconv.ParseInt(key, 10, 64); persist(id, st) }
	}
	if forget != nil {
		f = func(key string) { id, _ := strconv.ParseInt(key, 10, 64); forget(id) }
	}
	return NewMemberRegistry(p, f)
}

// 成员键由渠道 ID 和实际模型 ID 组成，旧调用的整数身份只用于兼容已有测试和调用方。
func NewMemberRegistry(persist func(string, State), forget func(string)) *Registry {
	return &Registry{m: map[string]*State{}, persist: persist, forget: forget}
}

// Restore seeds state loaded from the DB at startup (bypasses persistence).
func (r *Registry) Restore(identity any, st State) {
	id := fmt.Sprint(identity)
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := st
	r.m[id] = &cp
}

// Get returns a copy of one profile's state.
func (r *Registry) Get(identity any) State {
	id := fmt.Sprint(identity)
	r.mu.Lock()
	defer r.mu.Unlock()
	if st := r.m[id]; st != nil {
		return *st
	}
	return State{}
}

// IsOpen reports whether a profile is in its cooling-off window.
func (r *Registry) IsOpen(id any) bool { return r.Get(id).Open() }

// Pass records a successful call: clears the failure counters so a profile that
// recovers is fully trusted again (and drops the persisted row).
func (r *Registry) Pass(identity any) {
	r.opMu.Lock()
	defer r.opMu.Unlock()
	id := fmt.Sprint(identity)
	r.mu.Lock()
	st := r.m[id]
	if st == nil || (st.Fails == 0 && st.Trips == 0 && st.OpenUntil.IsZero()) {
		r.mu.Unlock()
		return // already clean — nothing to write
	}
	delete(r.m, id)
	forget := r.forget
	r.mu.Unlock()
	if forget != nil {
		forget(id)
	}
}

// Trip records a failed call. hard=true marks a deterministic failure (no credit,
// invalid key, missing model) which opens the breaker immediately; hard=false is a
// transient one (429 / 5xx / network) that needs softTripAfter in a row. Returns
// true when this call is what opened the breaker, so the caller can log it once.
func (r *Registry) Trip(identity any, errMsg string, hard bool) (tripped bool) {
	r.opMu.Lock()
	defer r.opMu.Unlock()
	id := fmt.Sprint(identity)
	r.mu.Lock()
	st := r.m[id]
	if st == nil {
		st = &State{}
		r.m[id] = st
	}
	st.Fails++
	st.LastError = errMsg
	st.LastAt = time.Now()
	softTrip := r.tripAfter()
	if hard || (softTrip > 0 && st.Fails >= softTrip) {
		st.Trips++
		st.OpenUntil = time.Now().Add(r.coolFor(st.Trips))
		st.Fails = 0 // counted into this trip; start fresh for the half-open probe
		tripped = true
	}
	snap := *st
	persist := r.persist
	r.mu.Unlock()
	if persist != nil {
		persist(id, snap)
	}
	return tripped
}

// Reset clears one profile's state — the UI's "立即恢复" action.
func (r *Registry) Reset(identity any) {
	r.opMu.Lock()
	defer r.opMu.Unlock()
	id := fmt.Sprint(identity)
	r.mu.Lock()
	delete(r.m, id)
	forget := r.forget
	r.mu.Unlock()
	if forget != nil {
		forget(id)
	}
}

// Snapshot returns a copy of all tracked state, for the status API.
// 批量恢复与 Trip/Pass 共用操作锁，持久化和内存删除不会被迟到写入交错。
func (r *Registry) ResetMatching(match func(string) bool, forget func() error) error {
	r.opMu.Lock()
	defer r.opMu.Unlock()
	if forget != nil {
		if err := forget(); err != nil {
			return err
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for key := range r.m {
		if match(key) {
			delete(r.m, key)
		}
	}
	return nil
}

func (r *Registry) Snapshot() map[string]State {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]State, len(r.m))
	for id, st := range r.m {
		out[id] = *st
	}
	return out
}
