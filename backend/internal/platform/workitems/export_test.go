package workitems

// EncodeCursorForTest exposes the cursor encoding to the external test package.
func EncodeCursorForTest(pos map[string]string) string { return encodeCursor(pos) }
