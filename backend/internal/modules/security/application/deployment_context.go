package application

import "context"

// DeploymentAdvisory is an applicable Advisory with open Findings for a product on a set of Devices (F9 G4: the
// security context of a Deployment).
type DeploymentAdvisory struct {
	ID              string
	Reference       string
	Title           string
	Severity        string
	Status          string
	KnownExploited  bool
	OpenFindings    int
	AffectedDevices int
}

// DeploymentContextTotals counts all advisories and open findings of the product on the Devices, beyond the list limit.
type DeploymentContextTotals struct {
	Advisories int
	Findings   int
}

// DeploymentAdvisories lists the applicable Advisories (applicable, remediation planned, remediating) that have open
// Findings for the product on the given Devices, with at most limit entries, and the totals. Device ids are an
// explicit scope: nothing outside them is read.
func (s *Service) DeploymentAdvisories(ctx context.Context, productID string, deviceIDs []string, limit int) ([]DeploymentAdvisory, DeploymentContextTotals, error) {
	reader, ok := s.store.(interface {
		DeploymentAdvisories(context.Context, string, []string, int) ([]DeploymentAdvisory, DeploymentContextTotals, error)
	})
	if !ok {
		return nil, DeploymentContextTotals{}, ErrNotFound
	}
	if !uuidPattern.MatchString(productID) || len(deviceIDs) == 0 {
		return nil, DeploymentContextTotals{}, nil
	}
	return reader.DeploymentAdvisories(ctx, productID, deviceIDs, limit)
}
