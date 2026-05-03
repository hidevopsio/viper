// Copyright © 2014 Steve Francia <spf@spf13.com>.
//
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package viper

// deepCopyValue recursively clones map and slice values so that the returned
// value shares no map or slice storage with the input. Scalar / non-container
// values pass through unchanged.
//
// Used at the read-path lock boundary (Get / Sub / AllSettings / find) so
// that callers can safely iterate, marshal, decode, or store the returned
// value without coordinating with Viper's mutex.
//
// Policy: configuration trees are assumed to be acyclic. A cyclic input
// would cause infinite recursion; this is consistent with how viper's
// upstream serializers (yaml/json/toml) and consumers (mapstructure)
// already behave.
func deepCopyValue(v interface{}) interface{} {
	switch t := v.(type) {
	case map[string]interface{}:
		return deepCopyStringMap(t)
	case map[interface{}]interface{}:
		out := make(map[interface{}]interface{}, len(t))
		for k, val := range t {
			out[k] = deepCopyValue(val)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(t))
		for i, val := range t {
			out[i] = deepCopyValue(val)
		}
		return out
	default:
		return v
	}
}

// deepCopyStringMap is a typed convenience wrapper around deepCopyValue for
// the common map[string]interface{} case.
func deepCopyStringMap(m map[string]interface{}) map[string]interface{} {
	if m == nil {
		return nil
	}
	out := make(map[string]interface{}, len(m))
	for k, v := range m {
		out[k] = deepCopyValue(v)
	}
	return out
}
