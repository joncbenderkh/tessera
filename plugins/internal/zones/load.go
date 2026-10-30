// SPDX-License-Identifier: GPL-3.0-or-later

// Package zones loads the time zones the first-party plugins' settings
// name, from one list, so a setting means the same on every system. It
// embeds the time zone database, for systems without one, such as
// Windows.
package zones

import (
	"fmt"
	"strings"
	"time"
	_ "time/tzdata"
)

// Load loads the time zone name, in any case, and returns it with its
// IANA spelling. Only the names in zoneList are accepted; "" and
// "Local" are the system's. Surrounding spaces are ignored.
func Load(name string) (*time.Location, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || strings.EqualFold(name, "Local") {
		return time.Local, "Local", nil
	}
	spelt, ok := names[strings.ToLower(name)]
	if !ok {
		return nil, name, fmt.Errorf("unknown time zone %q", name)
	}
	loc, err := time.LoadLocation(spelt)
	return loc, spelt, err
}

// Names is every zone name Load accepts besides Local, in their IANA
// spelling.
func Names() []string {
	return strings.Fields(zoneList)
}

// names maps the zones in zoneList, in lower case, to their IANA
// spelling. The system's zone database would otherwise accept names
// that others reject: misspelt ones on file systems that ignore case,
// as macOS's do by default, and its own extras such as posix/ paths.
var names = func() map[string]string {
	names := map[string]string{}
	for name := range strings.Lines(zoneList) {
		name = strings.TrimSpace(name)
		names[strings.ToLower(name)] = name
	}
	return names
}()
