package repository

// SetEntryReadLimit lowers the entry read bound for a test and returns the restore function.
func SetEntryReadLimit(n int) func() {
	old := entryReadLimit
	entryReadLimit = n
	return func() { entryReadLimit = old }
}
