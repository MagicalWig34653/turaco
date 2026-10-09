package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Persona classes. They follow docs/development/simulation-hospital.md.
const (
	ClassEmployee   = "employee"   // hospital staff, employee baseline
	ClassFirstLevel = "firstlevel" // first-level-support
	ClassTechnician = "technician" // it-specialist
	ClassLead       = "lead"       // it-site-lead
	ClassViewer     = "viewer"     // infrastructure engineers: tickets.view only
	ClassAdmin      = "admin"      // administrator and security analysts (audit log, users)
	ClassVendor     = "vendor"     // vendor-restricted: tasks only
)

// classOrder is the order in which --users picks personas (round-robin over classes).
var classOrder = []string{ClassEmployee, ClassTechnician, ClassFirstLevel, ClassLead, ClassAdmin, ClassViewer, ClassVendor}

// defaultWeights is the share of arrivals per class.
var defaultWeights = map[string]float64{
	ClassEmployee:   50,
	ClassFirstLevel: 14,
	ClassTechnician: 20,
	ClassLead:       8,
	ClassViewer:     3,
	ClassAdmin:      3,
	ClassVendor:     2,
}

const (
	simPassword   = "turaco-sim-password"
	adminLogin    = "devadmin"
	adminPassword = "turaco-dev-password"
)

// Roster returns the simulation personas; the admin password is configurable.
func Roster(adminPass string) []Persona {
	mk := func(class string, logins ...string) []Persona {
		var out []Persona
		for _, l := range logins {
			out = append(out, Persona{Login: l, Class: class, Password: simPassword})
		}
		return out
	}
	var r []Persona
	r = append(r, mk(ClassEmployee,
		"katharina.brandt", "marcel.voigt", "sven.lindner", "petra.ostermann", "elke.fischer", "sabine.hartmann", "jonas.wagner",
		"anja.reuter", "kemal.yilmaz", "thomas.krause", "birgit.lang", "monika.schaefer", "rainer.becker", "claudia.neumann")...)
	r = append(r, mk(ClassFirstLevel, "lena.bauer", "murat.demir")...)
	r = append(r, mk(ClassTechnician, "oliver.stein", "nadine.roth", "jens.albrecht", "henrik.vogel", "sandra.winter", "tobias.kraft")...)
	r = append(r, mk(ClassLead, "christian.hoffmann", "silke.brandl", "martin.kessler")...)
	r = append(r, mk(ClassViewer, "uwe.pohl", "mirja.engel")...)
	r = append(r, mk(ClassAdmin, "ines.falk", "deniz.arslan")...)
	r = append(r, Persona{Login: adminLogin, Class: ClassAdmin, Password: adminPass})
	r = append(r, mk(ClassVendor, "vendor.mueller", "vendor.schmidt")...)
	return r
}

// SelectPersonas filters the roster by class and limits it to n personas,
// taking them round-robin over the classes so every class stays represented.
// n <= 0 means all.
func SelectPersonas(roster []Persona, classes map[string]bool, n int) []Persona {
	by := map[string][]Persona{}
	for _, p := range roster {
		if len(classes) == 0 || classes[p.Class] {
			by[p.Class] = append(by[p.Class], p)
		}
	}
	var out []Persona
	for round := 0; ; round++ {
		added := false
		for _, c := range classOrder {
			if round < len(by[c]) {
				if n > 0 && len(out) >= n {
					return out
				}
				out = append(out, by[c][round])
				added = true
			}
		}
		if !added {
			return out
		}
	}
}

// ParseWeights parses "employee=60,technician=20" over the defaults.
func ParseWeights(spec string) (map[string]float64, error) {
	w := map[string]float64{}
	for k, v := range defaultWeights {
		w[k] = v
	}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			return nil, fmt.Errorf("invalid weight %q (class=number)", part)
		}
		if _, ok := defaultWeights[kv[0]]; !ok {
			return nil, fmt.Errorf("unknown class %q in --weights (%s)", kv[0], strings.Join(classOrder, ", "))
		}
		f, err := strconv.ParseFloat(kv[1], 64)
		if err != nil || f < 0 {
			return nil, fmt.Errorf("invalid weight %q", part)
		}
		w[kv[0]] = f
	}
	return w, nil
}

// ParseClasses parses a comma separated class list ("" = all).
func ParseClasses(spec string) (map[string]bool, error) {
	if strings.TrimSpace(spec) == "" {
		return nil, nil
	}
	out := map[string]bool{}
	for _, c := range strings.Split(spec, ",") {
		c = strings.TrimSpace(c)
		if _, ok := defaultWeights[c]; !ok {
			return nil, fmt.Errorf("unknown class %q (%s)", c, strings.Join(classOrder, ", "))
		}
		out[c] = true
	}
	return out, nil
}

// weighted is a cumulative-weight chooser.
type weighted struct {
	keys []string
	cum  []float64
}

func newWeighted(m map[string]float64) *weighted {
	w := &weighted{}
	for k, v := range m {
		if v > 0 {
			w.keys = append(w.keys, k)
		}
	}
	sort.Strings(w.keys)
	var c float64
	for _, k := range w.keys {
		c += m[k]
		w.cum = append(w.cum, c)
	}
	return w
}

func (w *weighted) empty() bool { return len(w.keys) == 0 }

func (w *weighted) pick(f float64) string {
	x := f * w.cum[len(w.cum)-1]
	i := sort.SearchFloat64s(w.cum, x)
	if i >= len(w.keys) {
		i = len(w.keys) - 1
	}
	return w.keys[i]
}
