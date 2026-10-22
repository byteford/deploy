package tf

import (
	"testing"

	tfjson "github.com/hashicorp/terraform-json"
)

func TestDiffAttributes_NestedMapChange(t *testing.T) {
	// Mirrors the real case that motivated this: a nomadpack_deployment
	// resource with a "vars" map where one nested key changed and the
	// rest didn't.
	before := map[string]interface{}{
		"id": "palworld",
		"vars": map[string]interface{}{
			"app_exporter_enabled": "false",
			"cpu":                  "2000",
		},
	}
	after := map[string]interface{}{
		"id": "palworld",
		"vars": map[string]interface{}{
			"app_exporter_enabled": "true",
			"cpu":                  "2000",
		},
	}

	changes := diffAttributes(before, after, nil, nil, nil)
	if len(changes) != 1 {
		t.Fatalf("got %d changes, want 1: %+v", len(changes), changes)
	}
	c := changes[0]
	if c.Path != "vars.app_exporter_enabled" {
		t.Errorf("Path = %q, want vars.app_exporter_enabled", c.Path)
	}
	if c.Before != `"false"` || c.After != `"true"` {
		t.Errorf("Before/After = %q/%q, want \"false\"/\"true\"", c.Before, c.After)
	}
}

func TestDiffAttributes_UnchangedFieldsExcluded(t *testing.T) {
	before := map[string]interface{}{"a": "1", "b": "2", "c": "3"}
	after := map[string]interface{}{"a": "1", "b": "changed", "c": "3"}

	changes := diffAttributes(before, after, nil, nil, nil)
	if len(changes) != 1 || changes[0].Path != "b" {
		t.Fatalf("expected exactly one change on path 'b', got %+v", changes)
	}
}

func TestDiffAttributes_SensitiveValueMasked(t *testing.T) {
	before := map[string]interface{}{"token": "old-secret-value"}
	after := map[string]interface{}{"token": "new-secret-value"}
	afterSensitive := map[string]interface{}{"token": true}

	changes := diffAttributes(before, after, nil, nil, afterSensitive)
	if len(changes) != 1 {
		t.Fatalf("got %d changes, want 1", len(changes))
	}
	c := changes[0]
	if c.Before == "old-secret-value" || c.After == "new-secret-value" {
		t.Fatalf("sensitive value leaked into the diff: %+v", c)
	}
	if c.Before != "(sensitive value)" || c.After != "(sensitive value)" {
		t.Errorf("Before/After = %q/%q, want both masked as (sensitive value)", c.Before, c.After)
	}
}

func TestDiffAttributes_BeforeSensitiveAlsoMasks(t *testing.T) {
	// BeforeSensitive (not just AfterSensitive) must also trigger masking
	// — a field can be marked sensitive on either side.
	before := map[string]interface{}{"token": "old-secret"}
	after := map[string]interface{}{"token": "new-secret"}
	beforeSensitive := map[string]interface{}{"token": true}

	changes := diffAttributes(before, after, nil, beforeSensitive, nil)
	if len(changes) != 1 || changes[0].Before != "(sensitive value)" || changes[0].After != "(sensitive value)" {
		t.Fatalf("expected masking from BeforeSensitive alone, got %+v", changes)
	}
}

func TestDiffAttributes_WholeSubtreeSensitivePropagates(t *testing.T) {
	// AfterSensitive = true (a bare bool, not a nested map) at a parent
	// level means "everything under here is sensitive" and must
	// propagate down to every leaf, not just get silently dropped.
	before := map[string]interface{}{"vars": map[string]interface{}{"token": "old", "other": "x"}}
	after := map[string]interface{}{"vars": map[string]interface{}{"token": "new", "other": "y"}}
	afterSensitive := map[string]interface{}{"vars": true}

	changes := diffAttributes(before, after, nil, nil, afterSensitive)
	if len(changes) != 2 {
		t.Fatalf("got %d changes, want 2", len(changes))
	}
	for _, c := range changes {
		if c.Before != "(sensitive value)" || c.After != "(sensitive value)" {
			t.Errorf("path %s not masked: %+v", c.Path, c)
		}
	}
}

func TestDiffAttributes_KnownAfterApply(t *testing.T) {
	// A field that HAD a real value and is now becoming unknown (e.g.
	// the resource is being replaced) is a genuinely meaningful thing to
	// surface: "id: "abc-123" -> (known after apply)".
	before := map[string]interface{}{"id": "abc-123"}
	after := map[string]interface{}{"id": nil} // placeholder; real value determined at apply
	afterUnknown := map[string]interface{}{"id": true}

	changes := diffAttributes(before, after, afterUnknown, nil, nil)
	if len(changes) != 1 {
		t.Fatalf("got %d changes, want 1: %+v", len(changes), changes)
	}
	if changes[0].Before != `"abc-123"` || changes[0].After != "(known after apply)" {
		t.Errorf("Before/After = %q/%q, want \"abc-123\"/(known after apply)", changes[0].Before, changes[0].After)
	}
}

func TestDiffAttributes_NewComputedOnlyFieldSkipped(t *testing.T) {
	// A field that never had a value (absent from Before entirely, e.g.
	// a provider-internal hash recomputed on every apply) and is simply
	// unknown until apply isn't a meaningful "change" to surface — it's
	// noise, exactly like "diff_hash = (known after apply)" showing up
	// on every single plan regardless of what a person actually changed.
	before := map[string]interface{}{}
	after := map[string]interface{}{"diff_hash": nil}
	afterUnknown := map[string]interface{}{"diff_hash": true}

	changes := diffAttributes(before, after, afterUnknown, nil, nil)
	if len(changes) != 0 {
		t.Errorf("expected the computed-only field to be skipped as noise, got %+v", changes)
	}
}

func TestDiffAttributes_UnknownButActuallyUnchangedSkipped(t *testing.T) {
	// AfterUnknown=true but before==after (e.g. both nil) shouldn't be
	// reported — nothing is actually changing from the user's
	// perspective, even though terraform marks the slot as unknown.
	before := map[string]interface{}{"x": nil}
	after := map[string]interface{}{"x": nil}
	afterUnknown := map[string]interface{}{"x": true}

	changes := diffAttributes(before, after, afterUnknown, nil, nil)
	if len(changes) != 0 {
		t.Errorf("expected no changes, got %+v", changes)
	}
}

func TestDiffAttributes_ListIndexChange(t *testing.T) {
	before := map[string]interface{}{"tags": []interface{}{"a", "b"}}
	after := map[string]interface{}{"tags": []interface{}{"a", "c"}}

	changes := diffAttributes(before, after, nil, nil, nil)
	if len(changes) != 1 || changes[0].Path != "tags[1]" {
		t.Fatalf("expected one change at tags[1], got %+v", changes)
	}
	if changes[0].Before != `"b"` || changes[0].After != `"c"` {
		t.Errorf("Before/After = %q/%q, want \"b\"/\"c\"", changes[0].Before, changes[0].After)
	}
}

func TestDiffAttributes_NumberFormatting(t *testing.T) {
	// terraform-json decodes JSON numbers as float64; a whole number
	// like 2000 must render as "2000", not "2000" with a spurious ".0"
	// tacked on some other way.
	before := map[string]interface{}{"cpu": float64(1000)}
	after := map[string]interface{}{"cpu": float64(2000)}

	changes := diffAttributes(before, after, nil, nil, nil)
	if len(changes) != 1 || changes[0].Before != "1000" || changes[0].After != "2000" {
		t.Fatalf("expected clean integer formatting, got %+v", changes)
	}
}

func TestDiffAttributes_NoChangesForIdenticalState(t *testing.T) {
	state := map[string]interface{}{"a": "1", "vars": map[string]interface{}{"b": "2"}}
	changes := diffAttributes(state, state, nil, nil, nil)
	if len(changes) != 0 {
		t.Errorf("expected no changes for identical before/after, got %+v", changes)
	}
}

func TestSummarize_PopulatesAttributesOnlyForUpdateAndReplace(t *testing.T) {
	plan := &tfjson.Plan{
		ResourceChanges: []*tfjson.ResourceChange{
			{
				Address: "nomadpack_deployment.palworld",
				Change: &tfjson.Change{
					Actions: tfjson.Actions{tfjson.ActionUpdate},
					Before:  map[string]interface{}{"vars": map[string]interface{}{"x": "old"}},
					After:   map[string]interface{}{"vars": map[string]interface{}{"x": "new"}},
				},
			},
			{
				Address: "nomad_job.new_thing",
				Change: &tfjson.Change{
					Actions: tfjson.Actions{tfjson.ActionCreate},
					Before:  nil,
					After:   map[string]interface{}{"x": "new"},
				},
			},
			{
				Address: "nomad_job.old_thing",
				Change: &tfjson.Change{
					Actions: tfjson.Actions{tfjson.ActionDelete},
					Before:  map[string]interface{}{"x": "old"},
					After:   nil,
				},
			},
		},
	}

	var adds, changes, destroys int
	out := summarize(plan, &adds, &changes, &destroys)
	if len(out) != 3 {
		t.Fatalf("got %d resource changes, want 3", len(out))
	}

	byAddr := map[string]ResourceChange{}
	for _, rc := range out {
		byAddr[rc.Address] = rc
	}

	if len(byAddr["nomadpack_deployment.palworld"].Attributes) != 1 {
		t.Errorf("update: expected 1 attribute change, got %+v", byAddr["nomadpack_deployment.palworld"].Attributes)
	}
	if len(byAddr["nomad_job.new_thing"].Attributes) != 0 {
		t.Errorf("create: expected no attribute diff (everything is new), got %+v", byAddr["nomad_job.new_thing"].Attributes)
	}
	if len(byAddr["nomad_job.old_thing"].Attributes) != 0 {
		t.Errorf("delete: expected no attribute diff (everything is gone), got %+v", byAddr["nomad_job.old_thing"].Attributes)
	}
}
