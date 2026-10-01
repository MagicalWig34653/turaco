package application

// SweepSafeguardMinimum is the number of missing objects a run may always
// sweep, however small the directory is.
const SweepSafeguardMinimum = 5

// SweepWithheld is the not-observed sweep safeguard. missing is the number of
// previously observed objects (identities or groups) absent from the snapshot,
// before the number of previously observed objects, maxPercent the configured
// limit (0-100). The sweep is withheld when more than SweepSafeguardMinimum
// objects are missing and they exceed maxPercent of those observed before.
func SweepWithheld(missing, observedBefore, maxPercent int) bool {
	return missing > SweepSafeguardMinimum && missing*100 > maxPercent*observedBefore
}

// EmailDecision is the outcome of DecideEmailChange.
type EmailDecision int

const (
	// EmailApply means the desired email may be stored.
	EmailApply EmailDecision = iota
	// EmailConflict means another User owns the desired email; the stored
	// value is kept and the account is recorded as an email_in_use conflict.
	EmailConflict
)

// DecideEmailChange decides whether a directory account may take an email
// address. desiredKey is the case-insensitive comparison key of the desired
// address as computed by PostgreSQL lower() (nil when the account has no
// email), ownerID the User currently owning that key ("" when nobody does) and
// userID the account's own User ("" for an account that does not exist yet, so
// any owner conflicts; D2 forbids linking by email). Keys must never be
// computed with Go string functions: PostgreSQL's lower() is what the unique
// index uses.
func DecideEmailChange(desiredKey *string, ownerID, userID string) EmailDecision {
	if desiredKey == nil || ownerID == "" || ownerID == userID {
		return EmailApply
	}
	return EmailConflict
}
