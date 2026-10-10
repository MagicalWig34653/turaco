// Package settings holds the editable runtime administration settings. Each setting is defined in code (key, type,
// default, bounds, owning module); the database stores only the values an administrator changed. Environment values
// stay the deployment-time fallback for settings that existed before (see Service.Stored).
package settings

import "time"

// Type is the value type of a setting.
type Type string

// Setting value types. JSON forms: bool is true/false, int is an integer, duration is an integer number of seconds,
// enum is one of the listed strings.
const (
	TypeBool     Type = "bool"
	TypeInt      Type = "int"
	TypeDuration Type = "duration"
	TypeEnum     Type = "enum"
)

// Setting keys.
const (
	KeyAuthSessionAbsoluteTimeout = "auth.session_absolute_timeout"
	KeyAuthEntraSignoutMode       = "auth.entra_signout_mode"
	KeyAuthEntraProvisioning      = "auth.entra_provisioning"
	KeyTeamsPersonalEnabled       = "teams.personal_enabled"
	KeyTeamsPersonalDefault       = "teams.personal_default"
	KeyTeamsCardsWithTitles       = "teams.cards_with_titles"
)

// Definition describes one setting. Default, Min and Max use the JSON form of the type: duration in seconds.
type Definition struct {
	Key  string `json:"key"`
	Type Type   `json:"type"`
	// Default is bool, int64 (int and duration seconds) or string (enum).
	Default any `json:"default"`
	// Min and Max bound int and duration values (inclusive); nil means unbounded.
	Min *int64 `json:"min,omitempty"`
	Max *int64 `json:"max,omitempty"`
	// Options lists the allowed values of an enum.
	Options []string `json:"options,omitempty"`
	// Description is the English reference text; the UI shows localized text keyed by Key.
	Description string `json:"description"`
	Module      string `json:"module"`
	// NotYetActive marks a setting that is stored but not yet read by any behavior.
	NotYetActive bool `json:"notYetActive"`
	// Sensitive marks a setting whose change needs an explicit warning in the UI (privacy impact).
	Sensitive bool `json:"sensitive,omitempty"`
}

func i64(v int64) *int64 { return &v }

// Definitions returns the code-defined settings in display order.
func Definitions() []Definition {
	return []Definition{
		{Key: KeyAuthSessionAbsoluteTimeout, Type: TypeDuration, Default: int64((8 * time.Hour).Seconds()),
			Min: i64(int64(time.Hour.Seconds())), Max: i64(int64((24 * time.Hour).Seconds())), Module: "auth",
			Description: "Maximum lifetime of a new session. Applies to sessions created after the change; when never set, SESSION_ABSOLUTE_TIMEOUT applies."},
		{Key: KeyAuthEntraSignoutMode, Type: TypeEnum, Default: "shared_only", Options: []string{"never", "shared_only", "always"}, Module: "auth", NotYetActive: true,
			Description: "Whether signing out of Turaco also ends the Microsoft Entra session."},
		{Key: KeyAuthEntraProvisioning, Type: TypeEnum, Default: "link_only", Options: []string{"link_only", "auto_employee"}, Module: "auth",
			Description: "Whether a first Entra sign-in of a member of the home tenant may create an employee account (no roles, no teams; refused when the email address is already used) or only links to an existing one."},
		{Key: KeyTeamsPersonalEnabled, Type: TypeBool, Default: true, Module: "teams", NotYetActive: true,
			Description: "Whether personal Microsoft Teams notifications are available at all."},
		{Key: KeyTeamsPersonalDefault, Type: TypeBool, Default: false, Module: "teams", NotYetActive: true,
			Description: "Whether personal Teams notifications are on for users who have not chosen."},
		{Key: KeyTeamsCardsWithTitles, Type: TypeBool, Default: false, Module: "teams", NotYetActive: true, Sensitive: true,
			Description: "Whether ticket titles appear in personal Teams cards. Titles can contain patient data; enable only after the data protection review."},
	}
}
