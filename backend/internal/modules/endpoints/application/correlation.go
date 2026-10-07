package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"
)

// Failure correlation (F9 G4). The job groups the failed and expired targets of a Deployment by error code (the
// provider's raw status), device model, manufacturer, OS version and ring and raises the Turaco-derived Endpoint
// Finding deployment_failure_cluster for every group that is both large enough and over-represented. It is Turaco's
// inference from its own data; the provider's own statements (provider_reported_error) and contradictions with the
// software inventory (deployment_evidence_conflict) are separate findings and are never merged with it.
//
// The step is idempotent and bounded: it reads at most MaxCorrelationDeployments Deployments per run, aggregates in
// the database and raises at most MaxClustersPerDeployment findings per Deployment. A cluster that no longer holds
// (or whose Deployment completed, was cancelled or is older than CorrelationRecentWindow) is resolved by the same run.
const (
	DeploymentCorrelationJobType    = "endpoints.deployment_correlation"
	DeploymentCorrelationJobTimeout = 4 * time.Minute
	DeploymentCorrelationInterval   = 10 * time.Minute
	// MinClusterTargets is the smallest number of failed targets that make a cluster.
	MinClusterTargets = 3
	// ClusterFailureSharePercent is the share of all failures a group must cover; ClusterGroupFailurePercent the share
	// of its own targets that failed (either one is enough, after MinClusterTargets).
	ClusterFailureSharePercent = 20
	ClusterGroupFailurePercent = 50
	MaxClustersPerDeployment   = 10
	MaxCorrelationDeployments  = 200
	// CorrelationRecentWindow is how long after its end a Deployment that finished with errors or failed is still correlated.
	CorrelationRecentWindow = 14 * 24 * time.Hour
	// maxClusterValueRunes bounds a dimension value stored in a finding.
	maxClusterValueRunes = 100

	FindingDeploymentFailureCluster = "deployment_failure_cluster"
	DeploymentCorrelationActor      = "deployment-correlation"
	EventDeploymentFailureCluster   = "DeploymentFailureClusterDetected"

	DimensionErrorCode    = "error_code"
	DimensionModel        = "model"
	DimensionManufacturer = "manufacturer"
	DimensionOSVersion    = "os_version"
	DimensionRing         = "ring"
)

// FailureGroup is the number of failed or expired targets (Failed) among the Total targets of one value of one
// dimension. Total is 0 for the error code, which has no group of its own. Key identifies the value (the ring id for
// the ring dimension); Value is what is shown.
type FailureGroup struct {
	Dimension string
	Key       string
	Value     string
	Failed    int
	Total     int
}

// FailureCluster is a FailureGroup that qualifies as a cluster.
type FailureCluster struct {
	FailureGroup
	SharePercent int
}

// FindingKey is the key of the cluster's finding within its Deployment.
func (c FailureGroup) FindingKey() string { return c.Dimension + ":" + c.Key }

// DetectClusters returns the groups that form a cluster, largest first. A group qualifies with at least
// MinClusterTargets failures and either ClusterFailureSharePercent of all failures or ClusterGroupFailurePercent of its
// own targets. A group that spans every target of the Deployment (a homogeneous fleet, a single ring) says nothing
// about the cause and is skipped; the error code, which has no group, is kept. At most MaxClustersPerDeployment are
// returned.
func DetectClusters(totalFailures, totalTargets int, groups []FailureGroup) []FailureCluster {
	var out []FailureCluster
	for _, g := range groups {
		if g.Failed < MinClusterTargets || g.Value == "" || totalFailures <= 0 {
			continue
		}
		if g.Total > 0 && g.Total >= totalTargets {
			continue
		}
		share := g.Failed * 100 / totalFailures
		byShare := g.Failed*100 >= ClusterFailureSharePercent*totalFailures
		byGroup := g.Total > 0 && g.Failed*100 >= ClusterGroupFailurePercent*g.Total
		if !byShare && !byGroup {
			continue
		}
		out = append(out, FailureCluster{FailureGroup: g, SharePercent: share})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Failed != out[j].Failed {
			return out[i].Failed > out[j].Failed
		}
		if out[i].Dimension != out[j].Dimension {
			return out[i].Dimension < out[j].Dimension
		}
		return out[i].Key < out[j].Key
	})
	if len(out) > MaxClustersPerDeployment {
		out = out[:MaxClustersPerDeployment]
	}
	return out
}

// cleanClusterValue trims a provider or inventory string for storage in a finding: no control characters, at most
// maxClusterValueRunes runes.
func cleanClusterValue(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > maxClusterValueRunes {
		s = string(r[:maxClusterValueRunes])
	}
	return s
}

// CorrelationStore is the persistence port of the correlation job and the reports.
type CorrelationStore interface {
	// CorrelationDeployments returns at most limit Deployments to correlate: running, paused, and completed with errors
	// or failed since recentSince, plus every Deployment with an open cluster finding (to resolve it).
	CorrelationDeployments(ctx context.Context, recentSince time.Time, limit int) ([]string, error)
	// FailureGroupsTx aggregates the targets of a Deployment: the failed and expired ones and the targets that count
	// (not cancelled, not applicable, not already satisfied), then the failure groups per dimension.
	FailureGroupsTx(ctx context.Context, tx pgx.Tx, deploymentID string) (totalFailures, totalTargets int, groups []FailureGroup, err error)
	OpenClusterFindingTx(ctx context.Context, tx pgx.Tx, deploymentID, key string, detail []byte) (id string, raised bool, err error)
	OpenClusterKeysTx(ctx context.Context, tx pgx.Tx, deploymentID string) ([]string, error)
	ResolveClusterFindingTx(ctx context.Context, tx pgx.Tx, deploymentID, key string) (bool, error)
	// EngineHaltedRingsTx lists the halted rings whose halt was made by the engine (not by a person).
	EngineHaltedRingsTx(ctx context.Context, tx pgx.Tx, deploymentID string) ([]HaltedRing, error)
	FollowupExistsTx(ctx context.Context, tx pgx.Tx, deploymentID, reason string, ringID *string) (bool, error)
	InsertFollowupTx(ctx context.Context, tx pgx.Tx, deploymentID, reason string, ringID *string, taskID string) error
	LockFollowupsTx(ctx context.Context, tx pgx.Tx, deploymentID string) error
	ReportStore
}

// HaltedRing is a ring halted by the engine with its reason code.
type HaltedRing struct {
	RingID string
	Reason string
}

// HandleDeploymentCorrelation is the job handler of DeploymentCorrelationJobType.
func (s *Service) HandleDeploymentCorrelation(ctx context.Context, job jobs.Job) error {
	return s.RunCorrelation(ctx, "job:"+job.ID)
}

// RunCorrelation correlates the failures of the candidate Deployments and creates their follow-up. A failing
// Deployment does not stop the others.
func (s *Service) RunCorrelation(ctx context.Context, corr string) error {
	ids, err := s.store.CorrelationDeployments(ctx, s.now().Add(-CorrelationRecentWindow), MaxCorrelationDeployments)
	if err != nil {
		return err
	}
	var errs []error
	for _, id := range ids {
		if err := s.correlateDeployment(ctx, id, corr); err != nil {
			errs = append(errs, fmt.Errorf("deployment %s: %w", id, err))
		}
	}
	return errors.Join(errs...)
}

func (s *Service) correlateDeployment(ctx context.Context, id, corr string) error {
	c := Caller{Actor: audit.SystemActor(DeploymentCorrelationActor), CorrelationID: corr}
	return s.store.InTx(ctx, func(tx pgx.Tx) error {
		// One correlation of a Deployment at a time (two workers): the follow-up check and insert must not interleave.
		if err := s.store.LockFollowupsTx(ctx, tx, id); err != nil {
			return err
		}
		d, err := s.store.DeploymentTx(ctx, tx, id)
		if err != nil {
			return err
		}
		recent := d.FinishedAt != nil && s.now().Sub(*d.FinishedAt) <= CorrelationRecentWindow
		active := d.Status == DeploymentRunning || d.Status == DeploymentPaused ||
			(d.Status == DeploymentCompletedWithError || d.Status == DeploymentFailed) && recent
		var clusters []FailureCluster
		if active {
			total, targets, groups, err := s.store.FailureGroupsTx(ctx, tx, id)
			if err != nil {
				return err
			}
			clusters = DetectClusters(total, targets, cleanGroups(groups))
		}
		if err := s.syncClusterFindings(ctx, tx, c, d, clusters); err != nil {
			return err
		}
		if !active || !d.CreateTasks {
			return nil
		}
		return s.followUp(ctx, tx, c, d, len(clusters) > 0)
	})
}

// syncClusterFindings raises the findings of the current clusters and resolves the open ones that no longer hold.
func (s *Service) syncClusterFindings(ctx context.Context, tx pgx.Tx, c Caller, d Deployment, clusters []FailureCluster) error {
	current := make([]string, 0, len(clusters))
	for _, cl := range clusters {
		key := cl.FindingKey()
		current = append(current, key)
		detail, err := json.Marshal(map[string]any{"deploymentId": d.ID, "dimension": cl.Dimension, "value": cl.Value,
			"failed": cl.Failed, "total": cl.Total, "sharePercent": cl.SharePercent})
		if err != nil {
			return err
		}
		fid, raised, err := s.store.OpenClusterFindingTx(ctx, tx, d.ID, key, detail)
		if err != nil {
			return err
		}
		if !raised {
			continue
		}
		if err := publish(ctx, tx, c, "EndpointFindingRaised", map[string]any{"findingId": fid, "deploymentId": d.ID, "kind": FindingDeploymentFailureCluster}); err != nil {
			return err
		}
		if err := publish(ctx, tx, c, EventDeploymentFailureCluster, map[string]any{"deploymentId": d.ID, "findingId": fid, "dimension": cl.Dimension, "failed": cl.Failed}); err != nil {
			return err
		}
		if err := audit.Record(ctx, tx, audit.Change{Action: "endpoints.deployment.failure_cluster_detected", TargetType: "deployment", TargetID: d.ID, Actor: c.Actor,
			CorrelationID: c.CorrelationID, Metadata: map[string]any{"findingId": fid, "dimension": cl.Dimension, "failed": cl.Failed, "total": cl.Total}}); err != nil {
			return err
		}
	}
	open, err := s.store.OpenClusterKeysTx(ctx, tx, d.ID)
	if err != nil {
		return err
	}
	for _, key := range open {
		if slices.Contains(current, key) {
			continue
		}
		resolved, err := s.store.ResolveClusterFindingTx(ctx, tx, d.ID, key)
		if err != nil {
			return err
		}
		if resolved {
			if err := audit.Record(ctx, tx, audit.Change{Action: "endpoints.deployment.failure_cluster_resolved", TargetType: "deployment", TargetID: d.ID, Actor: c.Actor,
				CorrelationID: c.CorrelationID, Metadata: map[string]any{"key": key, "deploymentStatus": d.Status}}); err != nil {
				return err
			}
		}
	}
	return nil
}

// cleanGroups cleans the provider and inventory strings of the groups (control characters, length) and drops empty ones.
// The ring key is an id and stays as it is.
func cleanGroups(groups []FailureGroup) []FailureGroup {
	out := make([]FailureGroup, 0, len(groups))
	for _, g := range groups {
		g.Value = cleanClusterValue(g.Value)
		if g.Dimension != DimensionRing {
			g.Key = g.Value
		}
		if g.Value == "" || g.Key == "" {
			continue
		}
		out = append(out, g)
	}
	return out
}
