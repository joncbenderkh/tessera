// SPDX-License-Identifier: GPL-3.0-or-later

package tessera

import (
	"regexp"
	"testing"
)

func TestVersionIsSemver(t *testing.T) {
	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(Version) {
		t.Fatalf("VERSION %q is not MAJOR.MINOR.PATCH", Version)
	}
}
