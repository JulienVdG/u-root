package cmdlinefilter

import (
	"fmt"
	"log"
	"strings"
	"unicode"

	"github.com/u-root/u-root/pkg/cmdline"
)

// Remove filter the given cmdline by removing fields.
func Remove(cl string, fields []string) string {
	var newCl []string

	// kernel variables must allow '-' and '_' to be equivalent in variable
	// names. We will replace dashes with underscores for processing.
	for _, f := range fields {
		f = strings.Replace(f, "-", "_", -1)
	}

	lastQuote := rune(0)
	quotedFieldsCheck := func(c rune) bool {
		switch {
		case c == lastQuote:
			lastQuote = rune(0)
			return false
		case lastQuote != rune(0):
			return false
		case unicode.In(c, unicode.Quotation_Mark):
			lastQuote = c
			return false
		default:
			return unicode.IsSpace(c)
		}
	}

	for _, flag := range strings.FieldsFunc(string(cl), quotedFieldsCheck) {

		// Split the flag into a key and value, setting value="1" if none
		split := strings.Index(flag, "=")

		if len(flag) == 0 {
			continue
		}
		var key string
		if split == -1 {
			key = flag
		} else {
			key = flag[:split]
		}
		canonicalKey := strings.Replace(key, "-", "_", -1)
		skip := false
		for _, f := range fields {
			if canonicalKey == f {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		newCl = append(newCl, flag)
	}
	return strings.Join(newCl, " ")
}

// Update get the kernel command line parameters and filter it:
// it removes parameters listed in 'remove' and append extra parameters from
// the 'append' and 'reuse' flags
func Update(cl, append string, remove, reuse []string) string {
	acl := ""
	if len(append) > 0 {
		acl = " " + append
	}
	for _, f := range reuse {
		value, present := cmdline.Flag(f)
		if present {
			log.Printf("Cmdline reuse: %s=%v", f, value)
			acl = fmt.Sprintf("%s %s=%s", acl, f, value)
		}
		log.Printf("appendCmdline : '%v'", acl)
	}

	return Remove(cl, remove) + acl
}
