// Package query is the platform query engine of ADR-0033: modules declare a
// Field Catalog per list Resource, clients send a Filter AST, and the engine
// validates it against the catalog the caller may use, compiles it to
// parameterised SQL and paginates it with signed keyset cursors.
//
// Trust model. Catalogs are trusted Go code. The client supplies only field
// keys, operator names and values; field keys select a catalog-declared SQL
// expression built from typed constructors (Col, Lower, Ordinal), operator
// names select fixed SQL fragments and every value is a bind parameter. There
// is no way to pass raw SQL, column names, functions or ORDER BY text.
//
// The module keeps ownership of its SELECT list, its mandatory visibility
// predicate (ANDed outside the user filter, so a filter can never widen it)
// and its row scanning. The engine builds the WHERE/ORDER BY/LIMIT parts and
// runs them in a read-only transaction under a statement timeout.
//
// The package imports no business module (make archcheck).
package query
