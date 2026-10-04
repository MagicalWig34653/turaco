package application

import (
	"context"
	"time"
)

// RiskReviewRecord is a current acceptance whose review is due. Its data stays
// in Security; consumers receive it through security/public with a read scope.
type RiskReviewRecord struct {
	FindingID        string
	FindingReference string
	AdvisoryID       string
	ReviewBy         time.Time
}
type riskReviewReader interface {
	RiskReviewsDue(context.Context, time.Time, time.Time, time.Time, string, int) ([]RiskReviewRecord, error)
}

func (s *Service) RiskReviewsDue(ctx context.Context, from, to, afterDate time.Time, afterID string, limit int) ([]RiskReviewRecord, error) {
	reader, ok := s.store.(riskReviewReader)
	if !ok {
		return nil, ErrNotFound
	}
	return reader.RiskReviewsDue(ctx, from, to, afterDate, afterID, limit)
}

// AffectedDeviceCounts batches a bounded set of Advisory summary counts.
func (s *Service) AffectedDeviceCounts(ctx context.Context, ids []string) (map[string]int, error) {
	reader, ok := s.store.(interface {
		AffectedDeviceCounts(context.Context, []string) (map[string]int, error)
	})
	if !ok {
		return nil, ErrNotFound
	}
	return reader.AffectedDeviceCounts(ctx, ids)
}

func (s *Service) ApplicableAdvisories(ctx context.Context) ([]Advisory, bool, error) {
	reader, ok := s.store.(interface {
		ApplicableAdvisories(context.Context) ([]Advisory, bool, error)
	})
	if !ok {
		return nil, false, ErrNotFound
	}
	return reader.ApplicableAdvisories(ctx)
}
