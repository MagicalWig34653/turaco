package application

// Diff classes (F6 slice 4). A diff compares what Turaco derives for two subjects; an unknown stays unknown and is
// never counted as equal or different.
const (
	DiffSame      = "same"
	DiffDifferent = "different"
	DiffOnlyLeft  = "only_left"
	DiffOnlyRight = "only_right"
)

// Dimension results of a Device diff item.
const (
	DimSame      = "same"
	DimDifferent = "different"
	DimUnknown   = "unknown"
)

// MaxDiffScan bounds the artifacts looked at for one page of a diff.
const MaxDiffScan = MaxViewScan

// DiffFilter selects the items of a diff.
type DiffFilter struct {
	Kind string
	// OnlyDifferences drops items that are the same on both sides. Items with an unknown dimension are kept: they
	// cannot be shown to be equal.
	OnlyDifferences bool
	Page            Page
}

// DiffSide keeps Assigned, Expected Applicable and Observed of one Device apart for an artifact.
type DiffSide struct {
	Assigned bool
	// AssignedUnknown is set when an include assignment might address the Device (its User is unknown).
	AssignedUnknown bool
	Assignments     []AssignedTarget
	Expected        ExpectedApplicability
	Observed        *ObservedState
	Mismatch        string
}

// DiffDimensions compares the three states of the two sides. Expected is unknown when either side is unknown.
type DiffDimensions struct {
	Assigned string
	Expected string
	Observed string
}

// DeviceDiffItem is one artifact of a Device vs Device diff.
type DeviceDiffItem struct {
	Artifact   Artifact
	Left       DiffSide
	Right      DiffSide
	Class      string
	Dimensions DiffDimensions
	// Uncertain is set when any dimension is unknown: the class holds for what is known.
	Uncertain bool
}

// DeviceDiff is one page of a Device vs Device diff.
type DeviceDiff struct {
	LeftID, LeftName   string
	RightID, RightName string
	Items              []DeviceDiffItem
	NextCursor         string
	// Truncated is set when the scan limit ended the page early (NextCursor continues) or a bounded directory input was cut.
	Truncated bool
}

// GroupDiffSide lists the assignments of one Directory Group (or its parents) for an artifact.
type GroupDiffSide struct {
	Assignments []AssignedTarget
}

// GroupDiffItem is one artifact of a Directory Group vs Directory Group diff. Differences names the assignment
// properties that differ (mode, intent, filter) when the artifact targets both groups.
type GroupDiffItem struct {
	Artifact    Artifact
	Left        GroupDiffSide
	Right       GroupDiffSide
	Class       string
	Differences []string
}

// GroupDiffRef names a compared Directory Group.
type GroupDiffRef struct {
	ID, ExternalID, Name string
}

// GroupDiff is one page of a Directory Group vs Directory Group diff.
type GroupDiff struct {
	Left, Right GroupDiffRef
	Items       []GroupDiffItem
	NextCursor  string
	// Truncated is set when the scan limit ended the page early or the group nesting lookup was cut.
	Truncated bool
}
