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
	RiskReviewsDue(context.Context, time.Time, time.Time) ([]RiskReviewRecord, error)
}

func (s *Service) RiskReviewsDue(ctx context.Context, from, to time.Time) ([]RiskReviewRecord, error) {
	reader, ok := s.store.(riskReviewReader)
	if !ok {
		return nil, ErrNotFound
	}
	return reader.RiskReviewsDue(ctx, from, to)
}
