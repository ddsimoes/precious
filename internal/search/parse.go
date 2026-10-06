package search

import (
	"net/url"
	"strconv"
	"unicode/utf8"

	"precious/internal/domain"
)

// Parse reads a search from URL query parameters:
//
//	source=ID  name=TEXT  ext=X…  file_kind=K…  min_size=N  max_size=N
//	year_from=Y  year_to=Y  category=C…  triage=T…  tag=ID…  decision=D…
//	within=ENTRY_ID  sort=bytes|files|newest|name  order=desc|asc
//
// Parameters marked … repeat, one value each. An empty value is the same as
// an absent parameter. cursor and limit are the caller's and ignored here.
// Any other parameter, a repeated single-valued one, or a bad value is
// invalid_request.
func Parse(v url.Values) (Query, error) {
	var q Query
	for key, vals := range v {
		var err error
		switch key {
		case "cursor", "limit":
		case "source":
			err = single(key, vals, func(s string) error { q.Source = domain.SourceID(s); return nil })
		case "name":
			err = single(key, vals, func(s string) error { q.Name = s; return nil })
		case "sort":
			err = single(key, vals, func(s string) error { q.Sort = s; return nil })
		case "order":
			err = single(key, vals, func(s string) error { q.Order = s; return nil })
		case "min_size":
			err = single(key, vals, func(s string) error { return parseInt(key, s, &q.MinSize) })
		case "max_size":
			err = single(key, vals, func(s string) error { return parseInt(key, s, &q.MaxSize) })
		case "year_from":
			err = single(key, vals, func(s string) error { return parseYear(key, s, &q.YearFrom) })
		case "year_to":
			err = single(key, vals, func(s string) error { return parseYear(key, s, &q.YearTo) })
		case "within":
			err = single(key, vals, func(s string) error {
				id, err := strconv.ParseInt(s, 10, 64)
				if err != nil || id <= 0 {
					return invalid("within must be an entry id, not %q", s)
				}
				e := domain.EntryID(id)
				q.Within = &e
				return nil
			})
		case "ext":
			q.Ext = appendValues(q.Ext, vals)
		case "file_kind":
			q.FileKinds = appendValues(q.FileKinds, vals)
		case "category":
			q.Categories = appendValues(q.Categories, vals)
		case "triage":
			q.Triages = appendValues(q.Triages, vals)
		case "decision":
			q.Decisions = appendValues(q.Decisions, vals)
		case "tag":
			for _, s := range vals {
				if s == "" {
					continue
				}
				id, err := strconv.ParseInt(s, 10, 64)
				if err != nil || id <= 0 {
					return Query{}, invalid("tag must be a tag id, not %q", s)
				}
				q.Tags = append(q.Tags, id)
			}
		default:
			err = invalid("unknown search parameter %q", key)
		}
		if err != nil {
			return Query{}, err
		}
	}
	if err := q.validate(); err != nil {
		return Query{}, err
	}
	return q, nil
}

// single applies set to the one non-empty value of a single-valued
// parameter.
func single(key string, vals []string, set func(string) error) error {
	if len(vals) > 1 {
		return invalid("%s is given more than once", key)
	}
	if len(vals) == 0 || vals[0] == "" {
		return nil
	}
	return set(vals[0])
}

func appendValues[T ~string](dst []T, vals []string) []T {
	for _, s := range vals {
		if s != "" {
			dst = append(dst, T(s))
		}
	}
	return dst
}

func parseInt(key, s string, dst **int64) error {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return invalid("%s must be an integer, not %q", key, s)
	}
	*dst = &n
	return nil
}

func parseYear(key, s string, dst **int) error {
	n, err := strconv.Atoi(s)
	if err != nil {
		return invalid("%s must be a year, not %q", key, s)
	}
	*dst = &n
	return nil
}

func invalid(format string, args ...any) error {
	return domain.Errorf(domain.CodeInvalidRequest, format, args...)
}

// validate checks every value of q, however it was made (Parse, or JSON in
// a selection command).
func (q Query) validate() error {
	if !utf8.ValidString(q.Name) {
		return invalid("name is not valid UTF-8")
	}
	for _, x := range q.Ext {
		if normExt(x) == "" {
			return invalid("empty extension")
		}
	}
	for _, k := range q.FileKinds {
		if _, err := domain.ParseFileKind(string(k)); err != nil {
			return err
		}
	}
	for _, c := range q.Categories {
		if _, err := domain.ParseCategory(string(c)); err != nil {
			return err
		}
	}
	for _, tr := range q.Triages {
		if _, err := domain.ParseTriage(string(tr)); err != nil {
			return err
		}
	}
	for _, d := range q.Decisions {
		if _, err := domain.ParseDecision(string(d)); err != nil {
			return err
		}
	}
	for _, t := range q.Tags {
		if t <= 0 {
			return invalid("tag must be a tag id, not %d", t)
		}
	}
	if q.MinSize != nil && *q.MinSize < 0 {
		return invalid("min_size must not be negative")
	}
	if q.MaxSize != nil && *q.MaxSize < 0 {
		return invalid("max_size must not be negative")
	}
	if q.MinSize != nil && q.MaxSize != nil && *q.MinSize > *q.MaxSize {
		return invalid("min_size is above max_size")
	}
	for _, y := range []*int{q.YearFrom, q.YearTo} {
		if y != nil && (*y < minYear || *y > maxYear) {
			return invalid("year %d is outside %d..%d", *y, minYear, maxYear)
		}
	}
	if q.YearFrom != nil && q.YearTo != nil && *q.YearFrom > *q.YearTo {
		return invalid("year_from is after year_to")
	}
	if q.Within != nil && *q.Within <= 0 {
		return invalid("within must be an entry id")
	}
	switch q.Sort {
	case "", SortBytes, SortFiles, SortNewest, SortName:
	default:
		return invalid("unknown sort %q", q.Sort)
	}
	switch q.Order {
	case "", OrderAsc, OrderDesc:
	default:
		return invalid("unknown order %q", q.Order)
	}
	return nil
}
