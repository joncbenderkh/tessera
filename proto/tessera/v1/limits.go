// SPDX-License-Identifier: GPL-3.0-or-later

package tesserav1

// The limits on a view in docs/protocol.md. The host refuses a view over
// any of them and reads no more views from the plugin until it restarts.
const (
	// MaxViewBytes is the largest WatchResponse or RenderResponse,
	// encoded, gRPC's default receive limit.
	MaxViewBytes = 4 << 20
	// MaxViewDepth is how deeply a view's widgets may nest: View.body is
	// depth 1, and each StackItem.widget adds 1.
	MaxViewDepth = 16
	// MaxViewWidgets is how many widgets a view may hold: View.body and
	// every StackItem.widget below it.
	MaxViewWidgets = 1024
	// MaxTableColumns is how many columns a table may have.
	MaxTableColumns = 64
)

// Measure returns how deeply w's widgets nest, how many there are, and
// the most columns any of its tables has, as the limits count them.
func Measure(w *Widget) (depth, count, columns int) {
	if w == nil {
		return 0, 0, 0
	}
	columns = len(w.GetTable().GetColumns())
	for _, item := range w.GetStack().GetItems() {
		d, n, c := Measure(item.GetWidget())
		depth, count, columns = max(depth, d), count+n, max(columns, c)
	}
	return 1 + depth, 1 + count, columns
}
