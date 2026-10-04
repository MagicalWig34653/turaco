package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/advisories"
	securityapp "github.com/MagicalWig34653/turaco/backend/internal/modules/security/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/wiring"
)

// runSecurity accepts the same bounded JSON envelope as POST /api/v1/security/advisories/import.
// Database access to turaco-admin is an operator capability; the mutation is audited as the CLI.
func runSecurity(ctx context.Context, e env, command string, args []string) error {
	if command != "import" || len(args) != 0 {
		return errUsage
	}
	const maxBody = 1 << 20
	raw, err := io.ReadAll(io.LimitReader(e.stdin, maxBody+1))
	if err != nil {
		return fmt.Errorf("read advisory import: %w", err)
	}
	if len(raw) > maxBody {
		return fmt.Errorf("advisory import exceeds 1 MB")
	}
	var body struct {
		Records []advisories.AdvisoryRecord `json:"records"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		return fmt.Errorf("decode advisory import: %w", err)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("advisory import must contain one JSON object")
	}
	if len(body.Records) == 0 || len(body.Records) > securityapp.MaxImportRecords {
		return fmt.Errorf("advisory import must contain 1-%d records", securityapp.MaxImportRecords)
	}
	inputs := make([]securityapp.AdvisoryInput, 0, len(body.Records))
	for _, r := range body.Records {
		inputs = append(inputs, securityapp.FromRecord(r))
	}
	result, err := wiring.Security(e.pool).Import(ctx, securityapp.Caller{Actor: audit.SystemActor("turaco-admin"),
		CorrelationID: fmt.Sprintf("security-import:%d", time.Now().UnixNano())}, securityapp.Principal{Manage: true}, inputs)
	if err != nil {
		return err
	}
	return json.NewEncoder(e.stdout).Encode(result)
}
