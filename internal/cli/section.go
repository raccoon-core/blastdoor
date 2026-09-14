package cli

import "strings"

// sectionID turns a unit path into a GitLab section id: prefix, then the
// unit with anything but letters, digits, '-', '_' and '.' folded to '-', so
// a unit path collapses to one token without breaking the section marker.
func sectionID(prefix, unit string) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		default:
			return '-'
		}
	}, unit)
	return prefix + "-" + safe
}
