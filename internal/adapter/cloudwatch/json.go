package cloudwatch

import "encoding/json"

// parseJSONRaw is a thin wrapper so tests and the main code share one path.
func parseJSONRaw(s string, out *map[string]any) error {
	return json.Unmarshal([]byte(s), out)
}
