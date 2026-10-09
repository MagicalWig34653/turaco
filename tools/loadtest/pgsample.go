package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// PGSample is one reading of pg_stat_activity, pg_locks and pg_stat_database.
type PGSample struct {
	T               float64 `json:"t"`
	Connections     int64   `json:"connections"`
	Active          int64   `json:"active"`
	IdleInTx        int64   `json:"idleInTransaction"`
	LockWaiters     int64   `json:"lockWaiters"`
	NotGranted      int64   `json:"locksNotGranted"`
	LongestQuerySec float64 `json:"longestQuerySec"`
	LongestXactSec  float64 `json:"longestTransactionSec"`
	CommitsPerSec   float64 `json:"commitsPerSec"`
	RollbacksPerSec float64 `json:"rollbacksPerSec"`
	CacheHitRatio   float64 `json:"cacheHitRatio"`
}

type dbStats struct {
	commit, rollback, blksRead, blksHit, deadlocks, tempFiles, tempBytes    int64
	tupReturned, tupFetched, tupInserted, tupUpdated, tupDeleted, conflicts int64
}

// PGSummary condenses the samples.
type PGSummary struct {
	Samples            int        `json:"samples"`
	MaxConnections     int64      `json:"maxConnections"`
	PeakConnections    int64      `json:"peakConnections"`
	PeakActive         int64      `json:"peakActive"`
	PeakIdleInTx       int64      `json:"peakIdleInTransaction"`
	PeakLockWaiters    int64      `json:"peakLockWaiters"`
	PeakNotGranted     int64      `json:"peakLocksNotGranted"`
	LongestQuerySec    float64    `json:"longestQuerySec"`
	LongestQueryText   string     `json:"longestQueryText,omitempty"`
	LongestXactSec     float64    `json:"longestTransactionSec"`
	CommitsPerSecAvg   float64    `json:"commitsPerSecAvg"`
	CommitsPerSecPeak  float64    `json:"commitsPerSecPeak"`
	Commits            int64      `json:"commits"`
	Rollbacks          int64      `json:"rollbacks"`
	Deadlocks          int64      `json:"deadlocks"`
	TempFiles          int64      `json:"tempFiles"`
	TempBytes          int64      `json:"tempBytes"`
	Conflicts          int64      `json:"conflicts"`
	TuplesInserted     int64      `json:"tuplesInserted"`
	TuplesUpdated      int64      `json:"tuplesUpdated"`
	TuplesDeleted      int64      `json:"tuplesDeleted"`
	CacheHitRatioMin   float64    `json:"cacheHitRatioMin"`
	Error              string     `json:"error,omitempty"`
	Timeline           []PGSample `json:"timeline,omitempty"`
	ConnectionsPercent float64    `json:"peakConnectionsPercent"`
}

// PGSampler polls Postgres statistics while the run is going.
type PGSampler struct {
	conn     *pgx.Conn
	interval time.Duration
	start    time.Time
	cancel   context.CancelFunc
	wg       sync.WaitGroup

	mu       sync.Mutex
	samples  []PGSample
	sum      PGSummary
	first    dbStats
	last     dbStats
	hasFirst bool
	lastAt   time.Time
}

// PGHost returns the host of a Postgres URL (for the loopback safety check).
func PGHost(url string) (string, error) {
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		return "", fmt.Errorf("invalid --pg-url: %w", err)
	}
	return cfg.Host, nil
}

// StartPGSampler connects and starts sampling every interval until Stop.
func StartPGSampler(ctx context.Context, url string, interval time.Duration) (*PGSampler, error) {
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("connect to Postgres: %w", err)
	}
	s := &PGSampler{conn: conn, interval: interval, start: time.Now()}
	_ = conn.QueryRow(ctx, `SELECT current_setting('max_connections')::bigint`).Scan(&s.sum.MaxConnections)
	sctx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.wg.Add(1)
	go s.loop(sctx)
	return s, nil
}

func (s *PGSampler) loop(ctx context.Context) {
	defer s.wg.Done()
	s.sample(ctx)
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.sample(ctx)
		}
	}
}

func (s *PGSampler) sample(ctx context.Context) {
	qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var p PGSample
	err := s.conn.QueryRow(qctx, `
SELECT count(*)::bigint,
       (count(*) FILTER (WHERE state = 'active'))::bigint,
       (count(*) FILTER (WHERE state LIKE 'idle in transaction%'))::bigint,
       (count(*) FILTER (WHERE wait_event_type = 'Lock'))::bigint,
       COALESCE(max(EXTRACT(EPOCH FROM now() - query_start)) FILTER (WHERE state = 'active'), 0)::float8,
       COALESCE(max(EXTRACT(EPOCH FROM now() - xact_start)), 0)::float8
  FROM pg_stat_activity
 WHERE datname = current_database() AND pid <> pg_backend_pid() AND backend_type = 'client backend'`).
		Scan(&p.Connections, &p.Active, &p.IdleInTx, &p.LockWaiters, &p.LongestQuerySec, &p.LongestXactSec)
	if err != nil {
		s.fail(err)
		return
	}
	if err := s.conn.QueryRow(qctx, `SELECT count(*)::bigint FROM pg_locks WHERE NOT granted`).Scan(&p.NotGranted); err != nil {
		s.fail(err)
		return
	}
	var d dbStats
	err = s.conn.QueryRow(qctx, `
SELECT xact_commit, xact_rollback, blks_read, blks_hit, deadlocks, temp_files, temp_bytes,
       tup_returned, tup_fetched, tup_inserted, tup_updated, tup_deleted, conflicts
  FROM pg_stat_database WHERE datname = current_database()`).
		Scan(&d.commit, &d.rollback, &d.blksRead, &d.blksHit, &d.deadlocks, &d.tempFiles, &d.tempBytes,
			&d.tupReturned, &d.tupFetched, &d.tupInserted, &d.tupUpdated, &d.tupDeleted, &d.conflicts)
	if err != nil {
		s.fail(err)
		return
	}
	now := time.Now()
	p.T = now.Sub(s.start).Seconds()

	s.mu.Lock()
	if !s.hasFirst {
		s.first, s.hasFirst = d, true
	} else if dt := now.Sub(s.lastAt).Seconds(); dt > 0 {
		p.CommitsPerSec = float64(d.commit-s.last.commit) / dt
		p.RollbacksPerSec = float64(d.rollback-s.last.rollback) / dt
		if reads := (d.blksRead - s.last.blksRead) + (d.blksHit - s.last.blksHit); reads > 0 {
			p.CacheHitRatio = float64(d.blksHit-s.last.blksHit) / float64(reads)
		} else {
			p.CacheHitRatio = 1
		}
	}
	s.last, s.lastAt = d, now
	longest := p.LongestQuerySec > s.sum.LongestQuerySec
	s.samples = append(s.samples, p)
	s.mu.Unlock()

	if longest && p.LongestQuerySec > 0.5 {
		var text string
		if err := s.conn.QueryRow(qctx, `
SELECT left(regexp_replace(query, '\s+', ' ', 'g'), 200) FROM pg_stat_activity
 WHERE state = 'active' AND datname = current_database() AND pid <> pg_backend_pid()
 ORDER BY query_start LIMIT 1`).Scan(&text); err == nil {
			s.mu.Lock()
			s.sum.LongestQuerySec, s.sum.LongestQueryText = p.LongestQuerySec, strings.TrimSpace(text)
			s.mu.Unlock()
		}
	}
}

func (s *PGSampler) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sum.Error == "" && !strings.Contains(err.Error(), "context canceled") {
		s.sum.Error = err.Error()
	}
}

// Stop ends sampling and returns the summary.
func (s *PGSampler) Stop(ctx context.Context) *PGSummary {
	s.cancel()
	s.wg.Wait()
	_ = s.conn.Close(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	sum := s.sum
	sum.Samples = len(s.samples)
	sum.CacheHitRatioMin = 1
	var commitSum float64
	var commitN int
	for _, p := range s.samples {
		sum.PeakConnections = maxI(sum.PeakConnections, p.Connections)
		sum.PeakActive = maxI(sum.PeakActive, p.Active)
		sum.PeakIdleInTx = maxI(sum.PeakIdleInTx, p.IdleInTx)
		sum.PeakLockWaiters = maxI(sum.PeakLockWaiters, p.LockWaiters)
		sum.PeakNotGranted = maxI(sum.PeakNotGranted, p.NotGranted)
		if p.LongestQuerySec > sum.LongestQuerySec {
			sum.LongestQuerySec = p.LongestQuerySec
		}
		if p.LongestXactSec > sum.LongestXactSec {
			sum.LongestXactSec = p.LongestXactSec
		}
		if p.CommitsPerSec > 0 {
			commitSum += p.CommitsPerSec
			commitN++
			if p.CommitsPerSec > sum.CommitsPerSecPeak {
				sum.CommitsPerSecPeak = p.CommitsPerSec
			}
			if p.CacheHitRatio < sum.CacheHitRatioMin {
				sum.CacheHitRatioMin = p.CacheHitRatio
			}
		}
	}
	if commitN > 0 {
		sum.CommitsPerSecAvg = commitSum / float64(commitN)
	}
	if s.hasFirst {
		sum.Commits = s.last.commit - s.first.commit
		sum.Rollbacks = s.last.rollback - s.first.rollback
		sum.Deadlocks = s.last.deadlocks - s.first.deadlocks
		sum.TempFiles = s.last.tempFiles - s.first.tempFiles
		sum.TempBytes = s.last.tempBytes - s.first.tempBytes
		sum.Conflicts = s.last.conflicts - s.first.conflicts
		sum.TuplesInserted = s.last.tupInserted - s.first.tupInserted
		sum.TuplesUpdated = s.last.tupUpdated - s.first.tupUpdated
		sum.TuplesDeleted = s.last.tupDeleted - s.first.tupDeleted
	}
	if sum.MaxConnections > 0 {
		sum.ConnectionsPercent = 100 * float64(sum.PeakConnections) / float64(sum.MaxConnections)
	}
	sum.Timeline = s.samples
	return &sum
}

func maxI(a, b int64) int64 {
	if b > a {
		return b
	}
	return a
}
