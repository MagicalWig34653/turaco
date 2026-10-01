package main

import "context"

// runEmergency implements the "emergency" command group. Owned by the
// authentication workstream; replaced by the real implementation.
func runEmergency(ctx context.Context, e env, command string, args []string) error {
	return errUsage
}
