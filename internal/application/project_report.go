package application

import (
	"context"
	"sort"
	"time"

	"github.com/mrhollen/ravn/internal/domain"
)

// Defaults applied when a report caller omits the tuning knobs:
//
//	StalledMinutes <= 0 → DefaultStalledMinutes
//	Since is nil        → now - DefaultSinceWindow (resolved against the clock)
const (
	DefaultStalledMinutes = 60
	DefaultSinceWindow    = 24 * time.Hour
)

// ReportOptions tunes one project report. Zero values select the defaults
// above — callers may pass the zero struct for a fully-defaulted digest.
type ReportOptions struct {
	StalledMinutes int      // idle window for stalled_tasks; <= 0 → DefaultStalledMinutes
	Since          *time.Time // lower bound for recently_completed (completed_at >= Since); nil → now - DefaultSinceWindow
}

// ProjectReport is the single-call project health digest. Compact summary rows
// only — deliberately no full task bodies: supervisors and agents consume this
// in ONE call instead of fetching the board plus get_task per item.
type ProjectReport struct {
	ProjectID domain.ID

	Counts ReportCounts // open+done (non-archived) tasks

	ActiveClaims   []ActiveClaim // leases present AND not yet expired, most urgent first
	ExpiredClaims  []ReportTaskRef // doing + lapsed lease (zombie claims), id ASC
	StalledTasks   []StalledTask // doing + idle strictly past StalledMinutes, most idle first
	Bottlenecks    []Bottleneck // open tasks blocking >=1 open task, blocks DESC
	OpenBlockedBy  []BlockedGroup // inverse of Bottlenecks: same order; each group = one blocker with the open tasks it blocks
	RecentlyCompleted []RecentlyCompleted // done + completed_at >= Since, newest first

	Workload map[string]int // durable assignee NAME → active (todo+doing) count; unassigned tasks bucket under "" (documented convention)
}

// ReportCounts tallies every NON-ARCHIVED task in the project (open AND done —
// done stays in counts). ByStatus always carries the three canonical statuses,
// zero when empty; ByPriority always carries all six priorities 0..5.
type ReportCounts struct {
	ByStatus   map[domain.Status]int
	ByPriority map[int]int
}

// ReportTaskRef is the minimal task pointer (id + title) shared by several digest sections.
type ReportTaskRef struct {
	ID    domain.ID
	Title string
}

// ActiveClaim is one live lease: a claim row present with expiry not before "now".
// remaining_sec = max(0, expiry - now), floor to whole seconds; it is 0 at the
// exact expiry instant (the same strict-< lapse convention as list's expired_claim filter).
type ActiveClaim struct {
	ID             domain.ID
	Title          string
	ClaimedBy      *domain.Actor
	ClaimExpiresAt time.Time
	RemainingSec   int64
}

// StalledTask is a doing task with no activity within the stalled window.
// LastActivityAt is the "last activity" defined on Report; IdleMinutes floors
// (now - LastActivityAt) to whole minutes.
type StalledTask struct {
	ID             domain.ID
	Title          string
	LastActivityAt time.Time
	IdleMinutes    int
}

// Bottleneck is an open task that blocks at least one other open task.
type Bottleneck struct {
	ID     domain.ID
	Title  string
	Blocks int // number of OPEN tasks blocked by this one
}

// BlockedGroup is the inverse bottleneck view: one blocker and the open tasks
// it blocks. A task with several open blockers appears under EACH of them.
type BlockedGroup struct {
	Blocker ReportTaskRef
	Blocked []ReportTaskRef
}

// RecentlyCompleted is a done task that finished inside the since window.
type RecentlyCompleted struct {
	ID          domain.ID
	Title       string
	CompletedAt time.Time
	Assignee    *domain.Actor // nil = unassigned
}

// Report computes the single-call project health digest for one project.
// Everything is gathered in ONE unit of work, so the snapshot is internally
// consistent (no interleaved mutations between sections).
//
// Scope: every NON-ARCHIVED task in the project — todo+doing ("open") plus
// done. Unknown project → domain.ErrNotFound (the handler renders a 404
// consistent with every other project endpoint).
//
// "Last activity" for stalled_tasks (documented choice): the task's most
// recent EVENT timestamp (MAX of the event-log ts) when the task has any
// events, else its row updated_at. Every parent-row mutation both stamps
// updated_at and appends an event at the same instant, and child-row activity
// (comments/artifacts/AC completion) is recorded as events too, so this is the
// most faithful "someone touched this task" signal in the schema. Heartbeats
// deliberately emit NO event and do NOT touch updated_at (a keep-alive is not a
// state change), so they never count as activity; that blind spot is bounded by
// the lease TTL because an actively-claimed task instead appears in
// active_claims while its lease is live. A doing task is stalled when its idle
// time is STRICTLY GREATER than StalledMinutes — at exactly the threshold it is
// still considered active (the same strict-bound convention as claim expiry).
//
// Bottlenecks / open_blocked_by use the relations table (kind 'blocked-by':
// blocked-task → blocker) restricted to BOTH ends being open (todo|doing,
// non-archived). "Open" excludes done by construction; archived rows are
// excluded because the task list itself is non-archived and the blockers query
// pre-filters archived/done targets.
//
// Workload buckets active (todo+doing) tasks by durable assignee NAME;
// unassigned tasks land under the empty-string bucket "" — the zero value,
// matching the AssigneeName filter convention (""). An empty workload map is
// returned as an empty object, never null.
func (s *Projects) Report(ctx context.Context, projectID domain.ID, o ReportOptions) (ProjectReport, error) {
	now := s.clock.Now()
	if o.StalledMinutes <= 0 {
		o.StalledMinutes = DefaultStalledMinutes
	}
	since := now.Add(-DefaultSinceWindow)
	if o.Since != nil {
		since = *o.Since
	}

	var rep ProjectReport
	var r Repos
	err := s.uow.Run(ctx, &r, func() error {
		if _, err := r.Projects.Get(ctx, projectID); err != nil { // 404 before touching task/relation/event tables
			return err
		}
		// Non-archived only (Archived zero value), no status filter → open + done;
		// Limit 0 = unbounded at the repository level (the clamping lives in
		// Tasks.List, which this use case does not go through).
		tasks, _, err := r.Tasks.List(ctx, TaskFilter{ProjectID: projectID})
		if err != nil {
			return err
		}

		rep.ProjectID = projectID
		rep.Counts.ByStatus = make(map[domain.Status]int, len(domain.CanonicalStatuses()))
		for _, st := range domain.CanonicalStatuses() {
			rep.Counts.ByStatus[st] = 0 // every canonical status always present, zero when empty
		}
		rep.Counts.ByPriority = make(map[int]int, int(domain.PriorityHighest-domain.PriorityLowest)+1)
		for p := domain.PriorityLowest; p <= domain.PriorityHighest; p++ {
			rep.Counts.ByPriority[int(p)] = 0 // all six priorities always present
		}
		rep.ActiveClaims = []ActiveClaim{}
		rep.ExpiredClaims = []ReportTaskRef{}
		rep.StalledTasks = []StalledTask{}
		rep.Bottlenecks = []Bottleneck{}
		rep.OpenBlockedBy = []BlockedGroup{}
		rep.RecentlyCompleted = []RecentlyCompleted{}
		rep.Workload = map[string]int{}

		title := make(map[domain.ID]string, len(tasks))
		var openIDs, doingIDs []domain.ID
		for _, t := range tasks {
			title[t.ID] = t.Title
			if _, canonical := rep.Counts.ByStatus[t.Status]; canonical { // non-canonical (Tier-2) statuses keep the map at exactly 3 keys
				rep.Counts.ByStatus[t.Status]++
			}
			rep.Counts.ByPriority[int(t.Priority)]++

			switch t.Status {
			case domain.StatusTodo, domain.StatusDoing: // "open": workload bucket + blocker math side
				key := "" // unassigned convention: the empty-string bucket (documented)
				if t.Assignee != nil {
					key = t.Assignee.Name
				}
				rep.Workload[key]++
				openIDs = append(openIDs, t.ID)
			case domain.StatusDone:
				if t.CompletedAt != nil && !t.CompletedAt.Before(since) { // completed_at >= since (window lower bound is inclusive)
					rep.RecentlyCompleted = append(rep.RecentlyCompleted, RecentlyCompleted{
						ID: t.ID, Title: t.Title, CompletedAt: *t.CompletedAt, Assignee: t.Assignee,
					})
				}
			}
			if t.Status == domain.StatusDoing {
				doingIDs = append(doingIDs, t.ID)
			}

			if !t.Claim.Present() {
				continue // leases are all-or-nothing: present ⇒ holder + expiry set
			}
			exp := t.Claim.Expiry()
			if !exp.Before(now) { // not lapsed: strict < is the lapse convention (list's expired_claim filter, Claim SQL)
				rep.ActiveClaims = append(rep.ActiveClaims, ActiveClaim{
					ID: t.ID, Title: t.Title, ClaimedBy: t.Claim.By(), ClaimExpiresAt: exp,
					RemainingSec: int64(exp.Sub(now).Seconds()), // >= 0 in this branch; floors to whole seconds
				})
			} else if t.Status == domain.StatusDoing { // zombie claim: doing + lapsed lease
				rep.ExpiredClaims = append(rep.ExpiredClaims, ReportTaskRef{ID: t.ID, Title: t.Title})
			}
		}
		sort.Slice(rep.ActiveClaims, func(i, j int) bool { // most urgent (soonest expiry) first; id breaks ties
			if !rep.ActiveClaims[i].ClaimExpiresAt.Equal(rep.ActiveClaims[j].ClaimExpiresAt) {
				return rep.ActiveClaims[i].ClaimExpiresAt.Before(rep.ActiveClaims[j].ClaimExpiresAt)
			}
			return rep.ActiveClaims[i].ID < rep.ActiveClaims[j].ID
		})
		sort.Slice(rep.ExpiredClaims, func(i, j int) bool { return rep.ExpiredClaims[i].ID < rep.ExpiredClaims[j].ID })

		// Stalled doing tasks: last activity = newest event ts when events exist, else updated_at.
		if len(doingIDs) > 0 {
			eventMax, err := r.Events.MaxAtByTasks(ctx, doingIDs)
			if err != nil {
				return err
			}
			window := time.Duration(o.StalledMinutes) * time.Minute
			for _, t := range tasks {
				if t.Status != domain.StatusDoing {
					continue
				}
				last := t.UpdatedAt // fallback: the task has no events at all (direct-insert / pre-log rows)
				if e, ok := eventMax[t.ID]; ok {
					last = e
				}
				idle := now.Sub(last)
				if idle > window { // strictly past the threshold — at exactly it, still active (documented)
					rep.StalledTasks = append(rep.StalledTasks, StalledTask{
						ID: t.ID, Title: t.Title, LastActivityAt: last, IdleMinutes: int(idle / time.Minute),
					})
				}
			}
			sort.Slice(rep.StalledTasks, func(i, j int) bool { // most idle first (oldest activity); id breaks ties
				if !rep.StalledTasks[i].LastActivityAt.Equal(rep.StalledTasks[j].LastActivityAt) {
					return rep.StalledTasks[i].LastActivityAt.Before(rep.StalledTasks[j].LastActivityAt)
				}
				return rep.StalledTasks[i].ID < rep.StalledTasks[j].ID
			})
		}

		// Bottlenecks + inverse grouping: outgoing blocked-by edges where BOTH ends are open.
		if len(openIDs) > 0 {
			blockedByTask, err := r.Tasks.OpenBlockersByTasks(ctx, openIDs) // keys = open dependents; values pre-filtered not-done/not-archived
			if err != nil {
				return err
			}
			blocks := map[domain.ID]int{}
			groups := map[domain.ID][]ReportTaskRef{}
			for depID, blockers := range blockedByTask {
				for _, b := range blockers {
					if b.Status != domain.StatusTodo && b.Status != domain.StatusDoing { // spec: both ends strictly open (todo|doing)
						continue
					}
					blocks[b.ID]++
					groups[b.ID] = append(groups[b.ID], ReportTaskRef{ID: depID, Title: title[depID]})
				}
			}
			for bID, n := range blocks { // >= 1 by construction
				rep.Bottlenecks = append(rep.Bottlenecks, Bottleneck{ID: bID, Title: title[bID], Blocks: n})
			}
			sort.Slice(rep.Bottlenecks, func(i, j int) bool { // most-blocked first; title then id break ties
				if rep.Bottlenecks[i].Blocks != rep.Bottlenecks[j].Blocks {
					return rep.Bottlenecks[i].Blocks > rep.Bottlenecks[j].Blocks
				}
				if rep.Bottlenecks[i].Title != rep.Bottlenecks[j].Title {
					return rep.Bottlenecks[i].Title < rep.Bottlenecks[j].Title
				}
				return rep.Bottlenecks[i].ID < rep.Bottlenecks[j].ID
			})
			for _, bn := range rep.Bottlenecks { // inverse grouping, SAME order as bottlenecks
				deps := groups[bn.ID]
				sort.Slice(deps, func(i, j int) bool {
					if deps[i].Title != deps[j].Title {
						return deps[i].Title < deps[j].Title
					}
					return deps[i].ID < deps[j].ID
				})
				rep.OpenBlockedBy = append(rep.OpenBlockedBy, BlockedGroup{
					Blocker: ReportTaskRef{ID: bn.ID, Title: bn.Title},
					Blocked: deps,
				})
			}
		}

		sort.Slice(rep.RecentlyCompleted, func(i, j int) bool { // newest first; id breaks same-instant ties
			if !rep.RecentlyCompleted[i].CompletedAt.Equal(rep.RecentlyCompleted[j].CompletedAt) {
				return rep.RecentlyCompleted[i].CompletedAt.After(rep.RecentlyCompleted[j].CompletedAt)
			}
			return rep.RecentlyCompleted[i].ID < rep.RecentlyCompleted[j].ID
		})
		return nil
	})
	if err != nil {
		return ProjectReport{}, err
	}
	return rep, nil
}
