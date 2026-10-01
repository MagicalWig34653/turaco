package main

import "context"

// runRole implements the "role" command group. Owned by the authorization
// workstream; replaced by the real implementation.
func runRole(ctx context.Context, e env, command string, args []string) error {
	return errUsage
}
