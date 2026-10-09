package query

// Validation limits. They are constants of the engine and mirrored in the
// catalog response; tests pin them.
const (
	MaxConditions      = 25
	MaxDepth           = 4
	MaxSortKeys        = 3
	MaxInValues        = 100
	MaxStringLength    = 200
	MaxSearchLength    = 100
	DefaultPageSize    = 50
	MaxPageSize        = 100
	MaxDefinitionBytes = 16 << 10
	MaxNDays           = 3650
	// CountCap is the largest exact count; larger sets report CountCap and capped.
	CountCap = 1000
	// MaxCost bounds the summed cost of all conditions and the search; a
	// single condition of cost slowCost or more may not sit in an OR group
	// with siblings.
	MaxCost  = 40
	slowCost = 8
)

// Limits returns the limits for the catalog response.
func Limits() LimitsInfo {
	return LimitsInfo{MaxConditions: MaxConditions, MaxDepth: MaxDepth, MaxSortKeys: MaxSortKeys, MaxInValues: MaxInValues,
		MaxStringLength: MaxStringLength, MaxSearchLength: MaxSearchLength, DefaultPageSize: DefaultPageSize,
		MaxPageSize: MaxPageSize, CountCap: CountCap}
}

// opCost is the cost model: an equality, range or null test costs 1 (the
// visibility predicate and statement timeout bound the rest), a prefix match
// 4 and a substring match slowCost unless a trigram index serves it (2), a
// multi-valued (EXISTS) test 4. Only operators the catalog exposes can be
// used, so a field cannot offer an expensive operator by accident.
func opCost(f *Field, op Op) int {
	switch {
	case f.Type == TypeTags:
		return 4
	case op == OpContains || op == OpNotContains || op == OpEndsWith:
		if f.Index == IndexTrigram {
			return 2
		}
		return slowCost
	case op == OpStartsWith:
		if f.Index == IndexTrigram {
			return 2
		}
		return 4
	}
	return 1
}
