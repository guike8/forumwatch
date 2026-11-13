package watch

import (
	"fmt"
	"regexp"
	"strings"
)

// AlertFilterSpec controls delivery, not collection or durable source revisions.
// Patterns use Go regular expressions; exclusions take precedence over inclusion.
type AlertFilterSpec struct {
	Include      string `json:"include,omitempty"`
	Exclude      string `json:"exclude,omitempty"`
	ExcludeTitle string `json:"exclude_title,omitempty"`
	ReadableOnly bool   `json:"readable_only,omitempty"`
	QuietSales   bool   `json:"quiet_sales,omitempty"`
}

// Match the app's title-only preset exactly. This heuristic can also exclude
// research/discussion titles mentioning combos or the listed account services.
var quietSalesTitleRE = regexp.MustCompile(`(?i)\b(combos?|combo lists?|(?:gmail|outlook|hotmail|yahoo|netflix|spotify).{0,40}accounts?)\b`)

type alertFilter struct {
	include, exclude, excludeTitle *regexp.Regexp
	readableOnly, quietSales       bool
}

func compileAlertFilter(spec AlertFilterSpec) (alertFilter, error) {
	f := alertFilter{readableOnly: spec.ReadableOnly, quietSales: spec.QuietSales}
	for _, field := range []struct {
		name, pattern string
		target        **regexp.Regexp
	}{
		{"include", spec.Include, &f.include},
		{"exclude", spec.Exclude, &f.exclude},
		{"exclude_title", spec.ExcludeTitle, &f.excludeTitle},
	} {
		if field.pattern == "" {
			continue
		}
		re, err := regexp.Compile(field.pattern)
		if err != nil {
			return f, fmt.Errorf("alert_filter.%s: %w", field.name, err)
		}
		*field.target = re
	}
	return f, nil
}

func (f alertFilter) matches(item Item) bool {
	body := item.Body
	if body == "" {
		body = item.Summary
	}
	body = strings.Join(strings.Fields(body), " ")
	if f.readableOnly && (item.Access == "restricted" || body == "") {
		return false
	}
	title := strings.Join(strings.Fields(item.Title), " ")
	text := title + "\n" + body
	return (f.include == nil || f.include.MatchString(text)) &&
		(f.exclude == nil || !f.exclude.MatchString(text)) &&
		(f.excludeTitle == nil || !f.excludeTitle.MatchString(title)) &&
		(!f.quietSales || !quietSalesTitleRE.MatchString(title))
}

// NewFilteredHub compiles an immutable policy before any recovery or collection.
// Only configured sources are delivered; removed-source state is still retained.
func NewFilteredHub(sources []Source) (*Hub, error) {
	h := NewHub()
	h.filters = make(map[string]alertFilter, len(sources))
	for _, source := range sources {
		f, err := compileAlertFilter(source.AlertFilter)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", source.ID, err)
		}
		h.filters[source.ID] = f
	}
	return h, nil
}

// RestoreSnapshot registers previously delivered matching revisions without
// replaying alerts. Later excluded edits can then remove clients' saved cards,
// even when asynchronous summaries are disabled. Call before collection starts.
func (h *Hub) RestoreSnapshot(store *Store) {
	h.markSummaryRecovery(store.Items(0))
}

func (h *Hub) Matches(item Item) bool {
	if item.Filtered {
		return false
	}
	if h.filters == nil {
		return true
	}
	f, ok := h.filters[item.Source]
	return ok && f.matches(item)
}

// Items filters before limiting: newer noise must not hide older matching posts.
func (h *Hub) Items(store *Store, limit int) []Item {
	items := make([]Item, 0)
	for _, item := range store.Items(0) {
		if h.Matches(item) {
			items = append(items, item)
			if limit > 0 && len(items) >= limit {
				break
			}
		}
	}
	return items
}
