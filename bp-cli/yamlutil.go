package bp

import "fmt"

// convertYAML converts map[interface{}]interface{} to map[string]interface{}.
// This keeps compatibility with code that may receive such maps.
func convertYAML(v interface{}) interface{} {
	switch t := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(t))
		for k, v := range t {
			out[k] = convertYAML(v)
		}
		return out
	case map[interface{}]interface{}:
		out := make(map[string]interface{}, len(t))
		for k, v := range t {
			ks, ok := k.(string)
			if !ok {
				ks = fmt.Sprintf("%v", k)
			}
			out[ks] = convertYAML(v)
		}
		return out
	case []interface{}:
		out := make([]interface{}, 0, len(t))
		for _, v := range t {
			out = append(out, convertYAML(v))
		}
		return out
	default:
		return t
	}
}
