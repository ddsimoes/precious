package jobs

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"precious/internal/clock"
	"precious/internal/domain"
	"precious/internal/store"
)

// Device sharing between priority classes (design D13, §8.4). The slots of a
// device key, the registry's or a placeholder "source:<id>", are shared by the
// jobs waiting for one: queued jobs that may be claimed, running jobs that
// gave their slot up in Runtime.Yield, and running jobs that moved to the
// key in Runtime.UseSource. When a slot is free, the arbiter grants it in a
// weighted rotation over the waiting classes and, within a class, to the job
// that has waited longest. Pool keys and the empty key of jobs without a
// source are not shared this way: their oldest due job goes first. The
// arbiter's state is in memory, so a restart starts every key on full
// credits, and order is re-established from the durable available_at.

// agingAfter is how long a job waits for its device before it ranks as
// interactive.
const agingAfter = 5 * time.Minute

// classCredits is each class's share of one round of the rotation: 4 grants
// for interactive work, 2 for reconciliation, and 1 for bulk work.
var classCredits = [...]int{ClassReconciliation: 2, ClassInteractive: 4, ClassBulk: 1}

// classOrder is the order in which the waiting classes that have credit left
// are served within a round.
var classOrder = [...]Class{ClassInteractive, ClassReconciliation, ClassBulk}

// sharedKey reports whether key is a device key whose slots the arbiter
// shares between classes: neither a pool key nor the empty key of jobs
// without a source.
func sharedKey(key string) bool {
	return key != "" && !strings.HasPrefix(key, poolKeyPrefix)
}

// waiter is a job waiting for a slot of a key.
type waiter struct {
	id    domain.JobID
	class Class
	// since is when the wait began, in Unix ms: available_at for a queued
	// job, the yield or move time for a running one.
	since int64
	// att is the running attempt blocked in Yield or UseSource; nil for a
	// queued job.
	att *attempt
	// slot is what a queued job takes when it is claimed.
	slot slot
}

// before orders waiters by how long they have waited, then by job ID.
func (w waiter) before(o waiter) bool {
	return w.since < o.since || (w.since == o.since && w.id < o.id)
}

// share is the rotation state of one device key: the credit each class has
// left in the current round.
type share struct {
	credit [len(classCredits)]int
}

func newShare() *share { return &share{credit: classCredits} }

// choice is the arbiter's pick among the waiters of a key.
type choice struct {
	i     int   // index of the waiter granted
	class Class // the class it is granted as: interactive once it has aged
	// spends is set when several classes wait: the grant spends credit of
	// class. A lone class is served without spending.
	spends bool
	// refill is set when every waiting class had spent its credit: the
	// grant starts a new round.
	refill bool
}

// choose picks the waiter that gets the key's next slot, without changing s.
// ws must not be empty.
func (s *share) choose(ws []waiter, now int64) choice {
	rank := func(w waiter) Class {
		if now-w.since > agingAfter.Milliseconds() {
			return ClassInteractive
		}
		return w.class
	}
	var waiting [len(classCredits)]bool
	classes := 0
	for _, w := range ws {
		if c := rank(w); !waiting[c] {
			waiting[c] = true
			classes++
		}
	}
	c := choice{i: -1, spends: classes > 1}
	credit := s.credit
	if c.spends {
		c.refill = true
		for cl, ok := range waiting {
			if ok && credit[cl] > 0 {
				c.refill = false
			}
		}
		if c.refill {
			credit = classCredits
		}
	}
	for _, cl := range classOrder {
		if waiting[cl] && (!c.spends || credit[cl] > 0) {
			c.class = cl
			break
		}
	}
	for i, w := range ws {
		if rank(w) == c.class && (c.i < 0 || w.before(ws[c.i])) {
			c.i = i
		}
	}
	return c
}

// spend records a grant chosen by choose.
func (s *share) spend(c choice) {
	if !c.spends {
		return
	}
	if c.refill {
		s.credit = classCredits
	}
	s.credit[c.class]--
}

// arbiter is a runner's device-sharing state. Runner.mu guards it.
type arbiter struct {
	shares map[string]*share
	// queued holds, per claim key, the oldest claimable queued job of each
	// kind, as the claim query of generation gen (Runner.queueGen) found them.
	queued map[string][]waiter
	gen    uint64
	// yielded holds, per device key, the attempts blocked in Yield, and
	// those that UseSource moved to the key.
	yielded map[string][]*attempt
}

func newArbiter() arbiter {
	return arbiter{shares: map[string]*share{}, queued: map[string][]waiter{}, yielded: map[string][]*attempt{}}
}

func (b *arbiter) share(key string) *share {
	s, ok := b.shares[key]
	if !ok {
		s = newShare()
		b.shares[key] = s
	}
	return s
}

// remove takes a out of its key's yielded attempts, if it is there.
func (b *arbiter) remove(a *attempt) {
	list := b.yielded[a.slot.key]
	for i, y := range list {
		if y == a {
			list = append(list[:i], list[i+1:]...)
			break
		}
	}
	if len(list) == 0 {
		delete(b.yielded, a.slot.key)
	} else {
		b.yielded[a.slot.key] = list
	}
}

// dropQueued forgets queued job id of key once it is claimed.
func (b *arbiter) dropQueued(key string, id domain.JobID) {
	list := b.queued[key]
	for i, w := range list {
		if w.id == id {
			b.queued[key] = append(list[:i:i], list[i+1:]...)
			return
		}
	}
}

// claimSnapshot is what a claim query filters by, taken under Runner.mu.
type claimSnapshot struct {
	kinds []any // registered kinds
	pools []any // (kind, "workers:<name>") pairs
	// exclusive and running are deviceSlots.blocked's sources.
	exclusive, running []domain.SourceID
	gen                uint64 // Runner.queueGen when taken
}

// snapshotLocked takes the claim snapshot. The caller holds r.mu.
func (r *Runner) snapshotLocked() claimSnapshot {
	snap := claimSnapshot{
		kinds: make([]any, 0, len(r.handlers)),
		pools: make([]any, 0, 2*len(r.pools)),
		gen:   r.queueGen.Load(),
	}
	for k := range r.handlers {
		snap.kinds = append(snap.kinds, string(k))
	}
	for k, key := range r.pools {
		snap.pools = append(snap.pools, string(k), key)
	}
	snap.exclusive, snap.running = r.slots.blocked()
	return snap
}

// head is a queued job that may be claimed next: the oldest due job of its
// kind under its claim key.
type head struct {
	id          domain.JobID
	kind        Kind
	source      domain.SourceID
	since       int64 // available_at
	pooled      bool
	key         string
	placeholder bool
}

// queryHeads lists, for every claim key, the oldest due queued job of each
// registered kind that source exclusivity lets start (deviceSlots.blocked),
// whether or not the key has a free slot. A job's key is its pool's
// "workers:<name>" for a pool kind (design D11), its source's device key
// (deviceKey), or the empty key for a job without a source. The query finds
// the oldest job of each kind per pool, per source, and among the jobs
// without a source; the sources' heads are then merged by device key, which
// only the registry knows, after the rows are closed so that it may read q.
func (r *Runner) queryHeads(ctx context.Context, q store.Queryer, snap claimSnapshot, now time.Time) ([]head, error) {
	if len(snap.kinds) == 0 {
		return nil, nil
	}
	// pools(kind, claim_key) maps each pool kind to its "workers:<name>" key;
	// it has no rows when no kind was registered with RegisterPool.
	with := `WITH pools(kind, claim_key) AS (SELECT NULL, NULL WHERE FALSE) `
	if n := len(snap.pools) / 2; n > 0 {
		with = `WITH pools(kind, claim_key) AS (VALUES ` +
			strings.TrimSuffix(strings.Repeat("(?, ?), ", n), ", ") + `) `
	}
	args := append(append([]any(nil), snap.pools...), clock.Millis(now))
	args = append(args, snap.kinds...)
	filter := ``
	if len(snap.exclusive) > 0 {
		filter = ` AND (pooled OR source_id IS NULL OR source_id NOT IN (` + placeholders(len(snap.exclusive)) + `))`
		for _, src := range snap.exclusive {
			args = append(args, string(src))
		}
	}
	heads, err := scanHeads(q.QueryContext(ctx, with+`SELECT id, kind, source_id, available_at, pooled, part_key FROM (
		SELECT *, ROW_NUMBER() OVER (PARTITION BY part_key, kind ORDER BY available_at, id) AS n FROM (
			SELECT j.id, j.kind, j.source_id, j.available_at, p.kind IS NOT NULL AS pooled,
				COALESCE(p.claim_key, 'source:' || j.source_id, '') AS part_key
			FROM jobs j LEFT JOIN pools p ON p.kind = j.kind
			WHERE j.state = 'queued' AND j.available_at <= ? AND j.kind IN (`+placeholders(len(snap.kinds))+`)
		) WHERE TRUE`+filter+`
	) WHERE n = 1`, args...))
	if err != nil {
		return nil, err
	}

	running := make(map[domain.SourceID]bool, len(snap.running))
	for _, src := range snap.running {
		running[src] = true
	}
	type group struct {
		key  string
		kind Kind
	}
	keys := map[domain.SourceID]slot{}
	first := map[group]int{} // index in out of the group's oldest head
	out := heads[:0]
	for _, h := range heads {
		if !h.pooled && h.source != "" {
			s, ok := keys[h.source]
			if !ok {
				s = r.deviceKey(ctx, q, h.source)
				keys[h.source] = s
			}
			if s.placeholder && running[h.source] {
				continue
			}
			h.key, h.placeholder = s.key, s.placeholder
		}
		g := group{h.key, h.kind}
		i, seen := first[g]
		switch {
		case !seen:
			first[g] = len(out)
			out = append(out, h)
		case h.since < out[i].since || (h.since == out[i].since && h.id < out[i].id):
			out[i] = h
		}
	}
	return out, nil
}

// scanHeads reads the rows of queryHeads' query; each head's key is its
// partition key until queryHeads resolves the sources' device keys.
func scanHeads(rows *sql.Rows, err error) ([]head, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []head
	for rows.Next() {
		var (
			h      head
			id     int64
			kind   string
			source sql.NullString
		)
		if err := rows.Scan(&id, &kind, &source, &h.since, &h.pooled, &h.key); err != nil {
			return nil, err
		}
		h.id, h.kind, h.source = domain.JobID(id), Kind(kind), domain.SourceID(source.String)
		out = append(out, h)
	}
	return out, rows.Err()
}

// setQueuedLocked replaces the arbiter's queued jobs with heads, read by a
// claim query of generation gen. The caller holds r.mu.
func (r *Runner) setQueuedLocked(heads []head, gen uint64) {
	queued := make(map[string][]waiter, len(heads))
	for _, h := range heads {
		s := slot{key: h.key, source: h.source, placeholder: h.placeholder}
		if h.pooled {
			s.source = "" // a pool job holds no slot of its source
		}
		queued[h.key] = append(queued[h.key], waiter{id: h.id, class: r.classes[h.kind], since: h.since, slot: s})
	}
	r.arb.queued, r.arb.gen = queued, gen
}

// refreshQueued reads the queued jobs again when a job became queued since
// the arbiter last read them, so that Yield sees every committed arrival.
func (r *Runner) refreshQueued(ctx context.Context) error {
	r.mu.Lock()
	if r.arb.gen == r.queueGen.Load() {
		r.mu.Unlock()
		return nil
	}
	snap := r.snapshotLocked()
	r.mu.Unlock()
	heads, err := r.queryHeads(ctx, r.store.Reader(), snap, r.clock.Now())
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.setQueuedLocked(heads, snap.gen)
	r.mu.Unlock()
	return nil
}

// waitersLocked lists the jobs waiting for a slot of key: its queued jobs
// that source exclusivity lets start, then its running attempts waiting in
// Yield, and those waiting in UseSource whose source lets them start as it
// would a claim. The caller holds r.mu.
func (r *Runner) waitersLocked(key string) []waiter {
	var ws []waiter
	for _, w := range r.arb.queued[key] {
		if r.slots.eligible(w.slot) {
			ws = append(ws, w)
		}
	}
	for _, a := range r.arb.yielded[key] {
		if a.moved && !r.slots.eligible(a.slot) {
			continue
		}
		ws = append(ws, waiter{id: a.job.ID, class: a.class, since: a.since, att: a})
	}
	return ws
}

// grant is the arbiter's decision for one free slot.
type grant struct {
	key string
	w   waiter
	c   choice
	sh  *share // nil for a key that is not shared
}

// nextGrantLocked decides who gets a free slot next: among the keys with a
// free slot and a waiter, the winner of each key (choose, or the oldest job
// for a key that is not shared), and of those the one that has waited
// longest. The caller holds r.mu.
func (r *Runner) nextGrantLocked(now int64) (best grant, found bool) {
	consider := func(key string) {
		if r.slots.busy[key] >= r.slots.capacityOf(key) {
			return
		}
		ws := r.waitersLocked(key)
		if len(ws) == 0 {
			return
		}
		g := grant{key: key}
		if sharedKey(key) {
			g.sh = r.arb.share(key)
			g.c = g.sh.choose(ws, now)
		} else {
			for i, w := range ws {
				if w.before(ws[g.c.i]) {
					g.c.i = i
				}
			}
		}
		g.w = ws[g.c.i]
		if !found || g.w.before(best.w) {
			best, found = g, true
		}
	}
	for key := range r.arb.queued {
		consider(key)
	}
	for key := range r.arb.yielded {
		if _, seen := r.arb.queued[key]; !seen {
			consider(key)
		}
	}
	return best, found
}

// grantLocked carries g out: a queued job takes its slot (false when it
// cannot), a yielded attempt gets its slot back, a moved attempt takes its
// new slot as a claim would, and the grant spends its class's credit. The
// caller holds r.mu.
func (r *Runner) grantLocked(g grant) bool {
	if a := g.w.att; a != nil {
		if a.moved {
			if !r.slots.tryAcquire(a.slot) {
				return false
			}
			a.moved = false
			a.yields = sharedKey(a.slot.key) && !a.slot.placeholder
		} else if !r.slots.reoccupy(g.key) {
			return false
		}
		r.arb.remove(a)
		a.vacated = false
		close(a.granted)
	} else {
		if !r.slots.tryAcquire(g.w.slot) {
			return false
		}
		r.arb.dropQueued(g.key, g.w.id)
	}
	if g.sh != nil {
		g.sh.spend(g.c)
	}
	return true
}

// offerLocked offers a's slot to the jobs waiting for its key (Yield) and
// reports whether a gave it up. a keeps its slot when its key has a free
// slot for them already, when nobody waits, or when a itself ranks first,
// which counts as a grant to a. Otherwise a vacates its slot and waits among
// the key's yielded attempts from now. An attempt whose earlier Yield or
// UseSource was interrupted holds no slot, so it waits for one again. The
// caller holds r.mu.
func (r *Runner) offerLocked(a *attempt, now int64) bool {
	key := a.slot.key
	if !a.vacated {
		if r.slots.busy[key] < r.slots.capacityOf(key) {
			return false
		}
		ws := r.waitersLocked(key)
		if len(ws) == 0 {
			return false
		}
		ws = append(ws, waiter{id: a.job.ID, class: a.class, since: now, att: a})
		sh := r.arb.share(key)
		c := sh.choose(ws, now)
		if ws[c.i].att == a {
			sh.spend(c)
			return false
		}
		r.slots.vacate(key)
		a.vacated = true
	}
	a.since, a.granted = now, make(chan struct{})
	r.arb.yielded[key] = append(r.arb.yielded[key], a)
	return true
}

// moveLocked moves a to next, a slot of another key (UseSource, design
// D13): a gives up what it holds as finishing would (releaseLocked), then
// waits from now among next's key's yielded attempts, holding no slot and
// counting as running in no source, until grantLocked takes next for it.
// The caller holds r.mu.
func (r *Runner) moveLocked(a *attempt, next slot, now int64) {
	r.releaseLocked(a)
	a.slot = next
	a.vacated, a.moved = true, true
	a.since, a.granted = now, make(chan struct{})
	r.arb.yielded[next.key] = append(r.arb.yielded[next.key], a)
}

// releaseLocked gives up everything a holds: its place among its key's
// yielded attempts, its key's slot unless it vacated it, and its count as
// running in its source (deviceSlots.release) unless a move left it holding
// nothing. The caller holds r.mu.
func (r *Runner) releaseLocked(a *attempt) {
	r.arb.remove(a)
	if !a.moved {
		r.slots.release(a.slot, !a.vacated)
	}
}
