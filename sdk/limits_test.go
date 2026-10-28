// SPDX-License-Identifier: GPL-3.0-or-later

package sdk

import (
	"log"
	"os"
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

// TestDroppedViewsLogOncePerReason logs a run of views dropped for the
// same reason once, so a plugin that keeps sending them does not push
// everything else out of its panel's log.
func TestDroppedViewsLogOncePerReason(t *testing.T) {
	var buf strings.Builder
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	views := make(chan View)
	s := newServer(Plugin{Views: views})
	done := make(chan struct{})
	go func() { s.receive(views); close(done) }()
	for _, v := range []string{"bad\xff", "bad\xff", "bad\xff", "good", "bad\xff"} {
		views <- View{Body: Text(v)}
	}
	close(views)
	<-done
	if n := strings.Count(buf.String(), "view dropped"); n != 2 {
		t.Errorf("logged %d drops, want 2:\n%s", n, buf.String())
	}
}
