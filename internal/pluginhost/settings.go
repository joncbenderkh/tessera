// SPDX-License-Identifier: GPL-3.0-or-later

package pluginhost

import (
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/joncbenderkh/tessera/internal/config"
)

// toStruct converts panel settings to the Struct that Configure carries.
func toStruct(s config.Settings) (*structpb.Struct, error) {
	m, _ := plain(s).(map[string]any)
	return structpb.NewStruct(m)
}

// plain replaces the named Settings type, which structpb does not
// accept, with map[string]any throughout v.
func plain(v any) any {
	switch v := v.(type) {
	case config.Settings:
		return plain(map[string]any(v))
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, e := range v {
			out[k] = plain(e)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, e := range v {
			out[i] = plain(e)
		}
		return out
	}
	return v
}
