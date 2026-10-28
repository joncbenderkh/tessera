// SPDX-License-Identifier: GPL-3.0-or-later

// Package sdk is the library plugin authors import. It hides the
// go-plugin/gRPC boilerplate of docs/protocol.md behind a [Plugin]
// description, view builders, a channel of views, and callbacks for
// settings and input events. It has no GUI dependency and builds with
// CGO_ENABLED=0.
//
// A plugin describes itself in a [Plugin], sends each new [View] on the
// channel it gives as [Plugin.Views], and calls [Serve]:
//
//	views := make(chan sdk.View)
//	go func() {
//		for t := range time.Tick(time.Second) {
//			views <- sdk.View{Title: "Clock", Body: sdk.Text(t.Format("15:04:05")).Size(sdk.SizeDisplay)}
//		}
//	}()
//	sdk.Serve(sdk.Plugin{Name: "my-clock", Version: "0.1.0", Views: views})
//
// The host shows only the latest view, so the SDK sends it the newest
// one and drops any it had no time to send. docs/plugins.md is the
// author guide, and sdk/example/counter a complete plugin.
package sdk
