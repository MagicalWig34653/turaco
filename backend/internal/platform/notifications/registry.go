package notifications

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// EmailText is the localized text of a notification email. Subject contains
// one %s, replaced by the notification title.
type EmailText struct {
	Subject string
	Intro   string
	Action  string // label of the link
}

// Category describes one kind of notification. The module that creates the
// notifications owns and registers it, so the platform needs no knowledge of
// its producers (docs/product/f2-work-foundation-design.md, design debt).
type Category struct {
	// Name is "<area>.<event>", for example "task.assigned".
	Name  string
	Owner string
	// Email holds the email texts per locale; "en" and "de" are required.
	Email map[string]EmailText
	// LinkType and LinkPath say where a notification of this category points:
	// LinkPath contains "{id}", replaced by the validated link id. A category
	// without a link leaves both empty.
	LinkType string
	LinkPath string
}

var categoryName = regexp.MustCompile(`^[a-z][a-z0-9]*(\.[a-z][a-z0-9_]*)+$`)

// requiredLocales must be present in every category's email texts.
var requiredLocales = []string{"en", "de"}

// Registry is the set of notification categories of a process. Both
// turaco-api (preferences, validation) and turaco-worker (creation, email)
// build the same registry from the modules' category lists.
type Registry struct {
	byName    map[string]Category
	linkPaths map[string]string
	names     []string
}

// NewRegistry validates the categories and builds the registry.
func NewRegistry(categories ...Category) (*Registry, error) {
	r := &Registry{byName: map[string]Category{}, linkPaths: map[string]string{}}
	for _, c := range categories {
		if !categoryName.MatchString(c.Name) {
			return nil, fmt.Errorf("notification category %q: name must look like area.event", c.Name)
		}
		if _, dup := r.byName[c.Name]; dup {
			return nil, fmt.Errorf("notification category %q is registered twice", c.Name)
		}
		if c.Owner == "" {
			return nil, fmt.Errorf("notification category %q: owner is required", c.Name)
		}
		for _, locale := range requiredLocales {
			t, ok := c.Email[locale]
			if !ok || t.Subject == "" || t.Intro == "" || t.Action == "" || strings.Count(t.Subject, "%s") != 1 {
				return nil, fmt.Errorf("notification category %q: complete %s email text with one %%s in the subject is required", c.Name, locale)
			}
		}
		if (c.LinkType == "") != (c.LinkPath == "") {
			return nil, fmt.Errorf("notification category %q: link type and path go together", c.Name)
		}
		if c.LinkType != "" {
			if strings.Count(c.LinkPath, "{id}") != 1 || !strings.HasPrefix(c.LinkPath, "/") {
				return nil, fmt.Errorf("notification category %q: link path must start with / and contain {id} once", c.Name)
			}
			if prev, ok := r.linkPaths[c.LinkType]; ok && prev != c.LinkPath {
				return nil, fmt.Errorf("notification category %q: link type %q is already mapped to %s", c.Name, c.LinkType, prev)
			}
			r.linkPaths[c.LinkType] = c.LinkPath
		}
		r.byName[c.Name] = c
		r.names = append(r.names, c.Name)
	}
	sort.Strings(r.names)
	return r, nil
}

// Valid reports whether the category is registered.
func (r *Registry) Valid(name string) bool {
	_, ok := r.byName[name]
	return ok
}

// Names returns the registered category names, sorted.
func (r *Registry) Names() []string { return append([]string(nil), r.names...) }

// Lookup returns a category.
func (r *Registry) Lookup(name string) (Category, bool) {
	c, ok := r.byName[name]
	return c, ok
}

// linkPath maps a notification link to an in-app path; unknown link types
// have none, and ids are validated so nothing user-controlled reaches a URL.
func (r *Registry) linkPath(linkType, linkID string) string {
	path, ok := r.linkPaths[linkType]
	if !ok || !validUUID(linkID) {
		return ""
	}
	return strings.Replace(path, "{id}", linkID, 1)
}
