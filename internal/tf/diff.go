package tf

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
)

// AttributeChange is one leaf attribute that actually changed between the
// before and after state of a resource, e.g. Path "vars.app_exporter_enabled"
// with Before `"false"` and After `"true"`. This is what turns a giant
// wall of raw `terraform plan` text (refresh noise, provider warnings,
// unrelated instructions) into just the handful of lines that actually
// matter for an update — the raw output stays available too (see
// PlanResult.RawOutput / the report's "full output" toggle), this is
// additive, not a replacement.
type AttributeChange struct {
	Path   string
	Before string
	After  string
}

// diffAttributes walks a resource's before/after state (as decoded by
// terraform-json — plain Go values: map[string]interface{},
// []interface{}, string, float64, bool, nil) in parallel and returns
// every leaf whose value actually changed. afterUnknown/beforeSensitive/
// afterSensitive mirror the same shape with bool leaves (or a bool
// `true` at a higher level meaning "everything under here"), matching
// terraform-json's own encoding for "known after apply" and sensitive
// values.
func diffAttributes(before, after, afterUnknown, beforeSensitive, afterSensitive interface{}) []AttributeChange {
	var out []AttributeChange
	walkDiff("", before, after, afterUnknown, beforeSensitive, afterSensitive, &out)
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func walkDiff(path string, before, after, afterUnknown, beforeSensitive, afterSensitive interface{}, out *[]AttributeChange) {
	sensitive := isTrue(beforeSensitive) || isTrue(afterSensitive)
	unknown := isTrue(afterUnknown)

	beforeMap, beforeIsMap := before.(map[string]interface{})
	afterMap, afterIsMap := after.(map[string]interface{})
	if beforeIsMap || afterIsMap {
		for _, key := range unionMapKeys(beforeMap, afterMap) {
			walkDiff(joinPath(path, key),
				mapChild(beforeMap, key), mapChild(afterMap, key),
				childOf(afterUnknown, key), childOf(beforeSensitive, key), childOf(afterSensitive, key),
				out)
		}
		return
	}

	beforeSlice, beforeIsSlice := before.([]interface{})
	afterSlice, afterIsSlice := after.([]interface{})
	if beforeIsSlice || afterIsSlice {
		n := len(beforeSlice)
		if len(afterSlice) > n {
			n = len(afterSlice)
		}
		for i := 0; i < n; i++ {
			var b, a interface{}
			if i < len(beforeSlice) {
				b = beforeSlice[i]
			}
			if i < len(afterSlice) {
				a = afterSlice[i]
			}
			walkDiff(fmt.Sprintf("%s[%d]", path, i),
				b, a,
				indexOf(afterUnknown, i), indexOf(beforeSensitive, i), indexOf(afterSensitive, i),
				out)
		}
		return
	}

	// Leaf value.
	if unknown {
		if !reflect.DeepEqual(before, after) {
			*out = append(*out, AttributeChange{Path: path, Before: formatLeaf(before, sensitive), After: "(known after apply)"})
		}
		return
	}
	if reflect.DeepEqual(before, after) {
		return
	}
	*out = append(*out, AttributeChange{Path: path, Before: formatLeaf(before, sensitive), After: formatLeaf(after, sensitive)})
}

func joinPath(base, key string) string {
	if base == "" {
		return key
	}
	return base + "." + key
}

func unionMapKeys(a, b map[string]interface{}) []string {
	seen := make(map[string]bool, len(a)+len(b))
	var keys []string
	for k := range a {
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	for k := range b {
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

func mapChild(m map[string]interface{}, key string) interface{} {
	if m == nil {
		return nil
	}
	return m[key]
}

// childOf reads one key out of an unknown/sensitive marker structure. A
// bare `true` at this level means "this whole subtree", so it propagates
// down rather than being treated as absent.
func childOf(v interface{}, key string) interface{} {
	switch m := v.(type) {
	case map[string]interface{}:
		return m[key]
	case bool:
		if m {
			return true
		}
	}
	return nil
}

func indexOf(v interface{}, i int) interface{} {
	switch s := v.(type) {
	case []interface{}:
		if i < len(s) {
			return s[i]
		}
	case bool:
		if s {
			return true
		}
	}
	return nil
}

func isTrue(v interface{}) bool {
	b, ok := v.(bool)
	return ok && b
}

// formatLeaf renders one leaf value the way a person wants to read it in
// a diff line — quoted strings (to make e.g. the empty string visible),
// trimmed-of-".0" numbers, and sensitive values masked exactly like
// Terraform's own CLI output masks them, since this can include Vault/
// Authentik tokens and the like.
func formatLeaf(v interface{}, sensitive bool) string {
	if sensitive {
		return "(sensitive value)"
	}
	switch x := v.(type) {
	case nil:
		return "(none)"
	case string:
		return strconv.Quote(x)
	case bool:
		return strconv.FormatBool(x)
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'g', -1, 64)
	default:
		return fmt.Sprintf("%v", x)
	}
}
