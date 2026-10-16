// Package syncer keeps the local database in step with OCAP: it polls the recordings list,
// downloads each new recording exactly once and stores its digest and statistics. It also
// runs the admin tasks (recompute, full resync) one at a time.
package syncer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/smitt14ua/ocap-statistics/internal/config"
	"github.com/smitt14ua/ocap-statistics/internal/digest"
	"github.com/smitt14ua/ocap-statistics/internal/ocap"
	"github.com/smitt14ua/ocap-statistics/internal/settings"
	"github.com/smitt14ua/ocap-statistics/internal/stats"
	"github.com/smitt14ua/ocap-statistics/internal/store"
)

// maxRecordingSize bounds the uncompressed size of a recording.
const maxRecordingSize = 4 << 30

// Task is a unit of work of the syncer.
type Task string

const (
	// TaskSync lists recordings and downloads the new ones.
	TaskSync Task = "sync"
	// TaskRecompute rebuilds statistics from stored digests (no downloads).
	TaskRecompute Task = "recompute"
	// TaskResync deletes everything and downloads all recordings again.
	TaskResync Task = "resync"
)

// Progress of the running task.
type Progress struct {
	Done  int `json:"done"`
	Total int `json:"total"`
}

// Status is exposed by the API.
type Status struct {
	Running       bool      `json:"running"`
	Task          Task      `json:"task,omitempty"`
	Progress      Progress  `json:"progress"`
	Queued        []Task    `json:"queued"`
	LastStart     time.Time `json:"lastStart,omitzero"`
	LastFinish    time.Time `json:"lastFinish,omitzero"`
	LastSuccess   time.Time `json:"lastSuccess,omitzero"`
	LastError     string    `json:"lastError,omitempty"`
	LastProcessed int       `json:"lastProcessed"`
	LastFailed    int       `json:"lastFailed"`
	NextRun       time.Time `json:"nextRun,omitzero"`
}

type Syncer struct {
	cfg      config.Config
	client   *ocap.Client
	store    *store.Store
	settings *settings.Manager
	log      *slog.Logger

	wake    chan struct{}
	mu      sync.Mutex
	pending map[Task]bool
	status  Status
}

func New(cfg config.Config, client *ocap.Client, st *store.Store, sm *settings.Manager, log *slog.Logger) *Syncer {
	return &Syncer{
		cfg:      cfg,
		client:   client,
		store:    st,
		settings: sm,
		log:      log,
		wake:     make(chan struct{}, 1),
		pending:  map[Task]bool{},
		status:   Status{Queued: []Task{}},
	}
}

func (s *Syncer) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.status
	st.Queued = []Task{}
	for _, t := range taskOrder {
		if s.pending[t] {
			st.Queued = append(st.Queued, t)
		}
	}
	return st
}

// taskOrder is the order queued tasks run in: a resync makes the others unnecessary, and a
// recompute should finish before new recordings are counted.
var taskOrder = []Task{TaskResync, TaskRecompute, TaskSync}

// Request queues a task; queuing a task that is already queued is a no-op.
func (s *Syncer) Request(t Task) {
	s.mu.Lock()
	s.pending[t] = true
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Trigger queues a sync.
func (s *Syncer) Trigger() { s.Request(TaskSync) }

// Run processes queued tasks until ctx is cancelled. With periodic set, a sync is queued
// every sync interval.
func (s *Syncer) Run(ctx context.Context, periodic bool) {
	s.Request(TaskRecompute) // statistics computed with older rules are rebuilt on start
	if periodic {
		s.Request(TaskSync)
	}
	for {
		for {
			t, ok := s.next()
			if !ok {
				break
			}
			s.runTask(ctx, t)
			if ctx.Err() != nil {
				return
			}
		}
		var tick <-chan time.Time
		var timer *time.Timer
		if periodic {
			s.mu.Lock()
			s.status.NextRun = time.Now().Add(s.cfg.Sync.Interval.D())
			s.mu.Unlock()
			timer = time.NewTimer(s.cfg.Sync.Interval.D())
			tick = timer.C
		}
		select {
		case <-ctx.Done():
			return
		case <-tick:
			s.Request(TaskSync)
		case <-s.wake:
			if timer != nil {
				timer.Stop()
			}
		}
	}
}

func (s *Syncer) next() (Task, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range taskOrder {
		if s.pending[t] {
			delete(s.pending, t)
			if t == TaskResync {
				// A resync downloads and computes everything anyway.
				delete(s.pending, TaskRecompute)
				delete(s.pending, TaskSync)
			}
			return t, true
		}
	}
	return "", false
}

// RunTask runs a task synchronously (used by -once and tests).
func (s *Syncer) RunTask(ctx context.Context, t Task) error {
	s.runTask(ctx, t)
	if e := s.Status().LastError; e != "" {
		return fmt.Errorf("%s", e)
	}
	return nil
}

func (s *Syncer) runTask(ctx context.Context, t Task) {
	s.mu.Lock()
	s.status.Running, s.status.Task = true, t
	s.status.Progress = Progress{}
	s.status.LastStart = time.Now()
	s.mu.Unlock()

	var processed, failed int
	var err error
	switch t {
	case TaskRecompute:
		processed, err = s.recompute(ctx)
	case TaskResync:
		s.log.Info("full resync: deleting stored recordings")
		if err = s.store.DeleteAll(ctx); err == nil {
			processed, failed, err = s.sync(ctx)
		}
	default:
		processed, failed, err = s.sync(ctx)
	}

	s.mu.Lock()
	s.status.Running, s.status.Task = false, ""
	s.status.LastFinish = time.Now()
	s.status.LastProcessed, s.status.LastFailed = processed, failed
	if err != nil {
		s.status.LastError = fmt.Sprintf("%s: %v", t, err)
	} else {
		s.status.LastError = ""
		s.status.LastSuccess = s.status.LastFinish
	}
	s.mu.Unlock()
	switch {
	case err != nil:
		s.log.Error("task failed", "task", t, "err", err)
	case processed > 0 || failed > 0:
		s.log.Info("task finished", "task", t, "processed", processed, "failed", failed)
	}
}

func (s *Syncer) setProgress(done, total int) {
	s.mu.Lock()
	s.status.Progress = Progress{Done: done, Total: total}
	s.mu.Unlock()
}

// recompute rebuilds statistics computed with other rules, using stored digests (no network).
func (s *Syncer) recompute(ctx context.Context) (int, error) {
	rules := s.settings.Rules()
	ids, err := s.store.StaleComputed(ctx, digest.Version, rules.Key())
	if err != nil || len(ids) == 0 {
		return 0, err
	}
	s.log.Info("recomputing statistics", "missions", len(ids))
	for i, id := range ids {
		if ctx.Err() != nil {
			return i, ctx.Err()
		}
		blob, err := s.store.Digest(ctx, id)
		if err != nil {
			return i, err
		}
		d, err := digest.Decode(blob)
		if err != nil {
			s.log.Warn("bad digest, will download again", "id", id, "err", err)
			_ = s.store.MarkError(ctx, id, fmt.Errorf("bad digest: %w", err), time.Now())
			continue
		}
		if err := s.store.Recompute(ctx, id, rules.Key(), stats.Compute(d, rules)); err != nil {
			return i, err
		}
		s.setProgress(i+1, len(ids))
	}
	return len(ids), nil
}

// sync lists recordings, removes the ones no longer listed and processes everything pending.
func (s *Syncer) sync(ctx context.Context) (processed, failed int, err error) {
	set := s.settings.Get()
	ops, err := s.listOperations(ctx, set.Tags, set.Since)
	if err != nil {
		return 0, 0, fmt.Errorf("list recordings: %w", err)
	}
	metas := make([]store.OperationMeta, 0, len(ops))
	keep := make(map[int64]bool, len(ops))
	for _, op := range ops {
		keep[op.ID] = true
		metas = append(metas, store.OperationMeta{
			ID: op.ID, Filename: op.Filename, MissionName: op.MissionName, WorldName: op.WorldName,
			Date: op.Day(), Tag: op.Tag, OCAPDuration: op.MissionDuration,
		})
	}
	added, err := s.store.UpsertOperations(ctx, metas)
	if err != nil {
		return 0, 0, err
	}
	if added > 0 {
		s.log.Info("new recordings", "count", added)
	}
	// An empty list most likely means an OCAP problem or a typo in tags: delete nothing then.
	if len(ops) > 0 {
		if n, err := s.store.DeleteMissing(ctx, keep); err != nil {
			return 0, 0, err
		} else if n > 0 {
			s.log.Info("removed recordings no longer listed", "count", n)
		}
	}

	counts, err := s.store.Counts(ctx)
	if err != nil {
		return 0, 0, err
	}
	total := counts.Pending + counts.Error
	for ctx.Err() == nil {
		batch, err := s.store.NextToFetch(ctx, s.cfg.Sync.Concurrency*4, s.cfg.Sync.MaxAttempts, digest.Version)
		if err != nil {
			return processed, failed, err
		}
		if len(batch) == 0 {
			break
		}
		p, f := s.processBatch(ctx, batch, func(done int) { s.setProgress(processed+failed+done, max(total, processed+failed+done)) })
		processed += p
		failed += f
		if p == 0 && f == 0 {
			break
		}
	}
	return processed, failed, ctx.Err()
}

func (s *Syncer) listOperations(ctx context.Context, tags []string, since string) ([]ocap.Operation, error) {
	if len(tags) == 0 {
		tags = []string{""}
	}
	seen := map[int64]bool{}
	var out []ocap.Operation
	for _, tag := range tags {
		ops, err := s.client.ListOperations(ctx, tag, since)
		if err != nil {
			return nil, err
		}
		for _, op := range ops {
			// OCAP matches tags as substrings; keep exact matches only.
			if tag != "" && !strings.EqualFold(op.Tag, tag) {
				continue
			}
			if op.Filename == "" || seen[op.ID] {
				continue
			}
			seen[op.ID] = true
			out = append(out, op)
		}
	}
	return out, nil
}

func (s *Syncer) processBatch(ctx context.Context, batch []store.MissionRef, progress func(done int)) (processed, failed int) {
	rules := s.settings.Rules()
	jobs := make(chan store.MissionRef)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for range min(s.cfg.Sync.Concurrency, len(batch)) {
		wg.Go(func() {
			for ref := range jobs {
				err := s.processOne(ctx, ref, rules)
				mu.Lock()
				if err != nil {
					failed++
				} else {
					processed++
				}
				progress(processed + failed)
				mu.Unlock()
				sleep(ctx, s.cfg.Sync.RequestDelay.D())
			}
		})
	}
	for _, ref := range batch {
		if ctx.Err() != nil {
			break
		}
		jobs <- ref
	}
	close(jobs)
	wg.Wait()
	return processed, failed
}

func (s *Syncer) processOne(ctx context.Context, ref store.MissionRef, rules *stats.Rules) error {
	start := time.Now()
	d, err := s.fetchDigest(ctx, ref.Filename)
	if err == nil {
		var blob []byte
		if blob, err = digest.Encode(d); err == nil {
			err = s.store.SaveResult(ctx, ref.ID, blob, digest.Version, d.MissionAuthor, rules.Key(), stats.Compute(d, rules))
		}
	}
	if err != nil {
		if ctx.Err() != nil {
			return err
		}
		if errors.Is(err, ocap.ErrNotFound) {
			s.log.Warn("recording file missing in OCAP, skipping it", "id", ref.ID, "file", ref.Filename)
			if mErr := s.store.MarkMissing(context.WithoutCancel(ctx), ref.ID, err); mErr != nil {
				s.log.Error("mark missing", "id", ref.ID, "err", mErr)
			}
			return err
		}
		// Back off exponentially: retryDelay, 2x, 4x, ...
		delay := s.cfg.Sync.RetryDelay.D() * time.Duration(1<<min(ref.Attempts, 6))
		s.log.Warn("recording failed", "id", ref.ID, "file", ref.Filename, "attempt", ref.Attempts+1, "err", err)
		if mErr := s.store.MarkError(context.WithoutCancel(ctx), ref.ID, err, time.Now().Add(delay)); mErr != nil {
			s.log.Error("mark error", "id", ref.ID, "err", mErr)
		}
		return err
	}
	s.log.Info("recording processed", "id", ref.ID, "file", ref.Filename, "took", time.Since(start).Round(time.Millisecond))
	return nil
}

// sleep waits for d or until ctx is done.
func sleep(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func (s *Syncer) fetchDigest(ctx context.Context, filename string) (*digest.Digest, error) {
	rc, err := s.client.OpenMission(ctx, filename)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	// A recording is a few hundred MB uncompressed at most; the limit stops a corrupt file
	// (or a gzip bomb) from exhausting memory.
	d, err := digest.Parse(io.LimitReader(rc, maxRecordingSize))
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", filename, err)
	}
	return d, nil
}
