// SPDX-License-Identifier: GPL-3.0-or-later

package sdk

import (
	"strings"
	"testing"
)

// TestCheckLimitsRefusesUnencodable drops a view gRPC could not send,
// which would otherwise fail every Watch and Render.
func TestCheckLimitsRefusesUnencodable(t *testing.T) {
	err := checkLimits(View{Body: Text("bad\xffname")}.proto())
	if err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Errorf("checkLimits = %v, want an invalid UTF-8 error", err)
	}
	if err := checkLimits(View{Body: Text("good")}.proto()); err != nil {
		t.Errorf("checkLimits = %v, want nil", err)
	}
}
