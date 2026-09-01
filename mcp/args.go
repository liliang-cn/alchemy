package mcp

import (
	"encoding/json"
	"fmt"
)

// unmarshalArgs reads the client's arguments into the map the tool layer takes.
//
// Every value becomes a string, and that is deliberate rather than lazy. The
// tool schemas declare every parameter as a string — a chunk index, a limit —
// because that is what a model reliably produces, and pkg/agenttool parses them
// with a fallback that is never zero. A client that helpfully sends `{"limit":
// 15}` as a JSON number would otherwise arrive as a float64 the tool layer
// reads as the empty string, and a limit of 15 would silently become the
// default. Numbers are rendered without a trailing ".0" for the same reason:
// "15.0" is not something Sscanf reads as 15.
func unmarshalArgs(raw json.RawMessage, into *map[string]any) error {
	var loose map[string]any
	if err := json.Unmarshal(raw, &loose); err != nil {
		return err
	}
	out := make(map[string]any, len(loose))
	for k, v := range loose {
		switch t := v.(type) {
		case nil:
			continue
		case string:
			out[k] = t
		case float64:
			if t == float64(int64(t)) {
				out[k] = fmt.Sprintf("%d", int64(t))
			} else {
				out[k] = fmt.Sprintf("%g", t)
			}
		case bool:
			out[k] = fmt.Sprintf("%t", t)
		default:
			b, err := json.Marshal(t)
			if err != nil {
				return fmt.Errorf("argument %q: %w", k, err)
			}
			out[k] = string(b)
		}
	}
	*into = out
	return nil
}
