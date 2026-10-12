package businesscalendars

import "testing"

func TestCalendarInputValidation(t *testing.T) {
	base := CalendarInput{Key: "weekday", Name: "Support", Timezone: "Europe/Berlin", WeeklyHours: map[int][]Interval{1: {{Start: 540, End: 720}, {Start: 780, End: 1020}}}}
	if err := validInput(base); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", " Support", "Support\u202e", "Support\nDesk"} {
		in := base
		in.Name = name
		if validInput(in) == nil {
			t.Errorf("accepted name %q", name)
		}
	}
	in := base
	in.WeeklyHours = map[int][]Interval{1: {{Start: 1320, End: 60}}}
	if validInput(in) == nil {
		t.Fatal("accepted overnight interval")
	}
}
