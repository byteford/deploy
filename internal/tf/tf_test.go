package tf

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"
)

func TestIsInitialized(t *testing.T) {
	repoRoot := t.TempDir()
	const dir = "services/example"
	full := filepath.Join(repoRoot, dir)
	if err := os.MkdirAll(full, 0o755); err != nil {
		t.Fatal(err)
	}

	runner := &Runner{} // execPath unused by IsInitialized

	if runner.IsInitialized(repoRoot, dir) {
		t.Fatal("should not report initialized before .terraform/ exists")
	}
	if err := os.MkdirAll(filepath.Join(full, ".terraform"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !runner.IsInitialized(repoRoot, dir) {
		t.Fatal("should report initialized once .terraform/ exists")
	}
}

func TestIsInitialized_FileNotDirIsNotInitialized(t *testing.T) {
	// A stray file named .terraform (not a directory) shouldn't count —
	// guards against a naive os.Stat-only check.
	repoRoot := t.TempDir()
	const dir = "services/example"
	full := filepath.Join(repoRoot, dir)
	if err := os.MkdirAll(full, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(full, ".terraform"), []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := &Runner{}
	if runner.IsInitialized(repoRoot, dir) {
		t.Fatal(".terraform as a plain file must not count as initialized")
	}
}

func TestActionOf(t *testing.T) {
	cases := []struct {
		name    string
		actions tfjson.Actions
		want    string
	}{
		{"no-op", tfjson.Actions{tfjson.ActionNoop}, "no-op"},
		{"create", tfjson.Actions{tfjson.ActionCreate}, "create"},
		{"delete", tfjson.Actions{tfjson.ActionDelete}, "delete"},
		{"update", tfjson.Actions{tfjson.ActionUpdate}, "update"},
		{"destroy-before-create replace", tfjson.Actions{tfjson.ActionDelete, tfjson.ActionCreate}, "replace"},
		{"create-before-destroy replace", tfjson.Actions{tfjson.ActionCreate, tfjson.ActionDelete}, "replace"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := actionOf(c.actions); got != c.want {
				t.Errorf("actionOf(%v) = %q, want %q", c.actions, got, c.want)
			}
		})
	}
}

func TestSummarize_CountsAndSkipsNoOps(t *testing.T) {
	plan := &tfjson.Plan{
		ResourceChanges: []*tfjson.ResourceChange{
			{Address: "nomad_job.a", Change: &tfjson.Change{Actions: tfjson.Actions{tfjson.ActionCreate}}},
			{Address: "nomad_job.b", Change: &tfjson.Change{Actions: tfjson.Actions{tfjson.ActionUpdate}}},
			{Address: "nomad_job.c", Change: &tfjson.Change{Actions: tfjson.Actions{tfjson.ActionDelete}}},
			{Address: "nomad_job.d", Change: &tfjson.Change{Actions: tfjson.Actions{tfjson.ActionDelete, tfjson.ActionCreate}}},
			{Address: "nomad_job.e", Change: &tfjson.Change{Actions: tfjson.Actions{tfjson.ActionNoop}}},
		},
	}

	var adds, changes, destroys int
	out := summarize(plan, &adds, &changes, &destroys)

	if adds != 1 {
		t.Errorf("adds = %d, want 1", adds)
	}
	if changes != 2 { // update + replace both count as "changes"
		t.Errorf("changes = %d, want 2", changes)
	}
	if destroys != 1 {
		t.Errorf("destroys = %d, want 1", destroys)
	}
	if len(out) != 4 {
		t.Fatalf("expected 4 non-no-op resource changes, got %d: %+v", len(out), out)
	}
	for _, rc := range out {
		if rc.Address == "nomad_job.e" {
			t.Error("no-op resource must be excluded from the change list")
		}
	}
}

func TestSummarize_EmptyPlanHasNoChanges(t *testing.T) {
	plan := &tfjson.Plan{}
	var adds, changes, destroys int
	out := summarize(plan, &adds, &changes, &destroys)
	if len(out) != 0 || adds != 0 || changes != 0 || destroys != 0 {
		t.Errorf("expected all-zero result for an empty plan, got out=%v adds=%d changes=%d destroys=%d", out, adds, changes, destroys)
	}
}

func TestIsSavedPlanUnsupported(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"exact terraform wording", errors.New(`Error: Saving a generated plan is currently not supported

The "remote" backend does not support saving the generated execution
plan locally at this time.`), true},
		{"unrelated error", errors.New("exit status 1: some other failure"), false},
		{"different error entirely", errors.New("connection refused"), false},
	}
	for _, c := range cases {
		if got := isSavedPlanUnsupported(c.err); got != c.want {
			t.Errorf("%s: isSavedPlanUnsupported(%v) = %v, want %v", c.name, c.err, got, c.want)
		}
	}
}

func TestParseRemotePlanURL(t *testing.T) {
	cases := []struct {
		name   string
		output string
		want   string
	}{
		{
			name: "real Terraform Cloud remote-execution wording",
			output: `Running plan in Terraform Cloud. Output will stream here. Pressing Ctrl-C
will stop streaming the logs, but will not stop the plan running remotely.

Preparing the remote plan...

To view this run in a browser, visit:
https://app.terraform.io/app/my-org/my-workspace/runs/run-CH3nkB1CV2NsSjxE

Waiting for the plan to start...
`,
			want: "https://app.terraform.io/app/my-org/my-workspace/runs/run-CH3nkB1CV2NsSjxE",
		},
		{name: "no such line", output: "Plan: 2 to add, 0 to change, 0 to destroy.\n", want: ""},
		{name: "empty output", output: "", want: ""},
	}
	for _, c := range cases {
		if got := ParseRemotePlanURL(c.output); got != c.want {
			t.Errorf("%s: ParseRemotePlanURL(...) = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestParsePlanSummaryLine(t *testing.T) {
	// "Plan: 2 to add, 0 to change, 0 to destroy." — captured from a
	// real `terraform plan -no-color` run (the exact flag terraform-exec
	// itself always passes), not hand-typed, so this is the actual
	// format being parsed rather than a guess at it.
	const realOutput = `terraform_data.a: Refreshing state... [id=abc]

Terraform used the selected providers to generate the following execution
plan. Resource actions are indicated with the following symbols:
  + create

Terraform will perform the following actions:

  # terraform_data.b will be created
  + resource "terraform_data" "b" {
      + id     = (known after apply)
    }

Plan: 2 to add, 0 to change, 0 to destroy.
`
	adds, changes, destroys := parsePlanSummaryLine(realOutput)
	if adds != 2 || changes != 0 || destroys != 0 {
		t.Errorf("got adds=%d changes=%d destroys=%d, want 2,0,0", adds, changes, destroys)
	}
}

func TestParsePlanSummaryLine_AllThreeCounts(t *testing.T) {
	adds, changes, destroys := parsePlanSummaryLine("blah blah\nPlan: 3 to add, 1 to change, 2 to destroy.\nmore text")
	if adds != 3 || changes != 1 || destroys != 2 {
		t.Errorf("got adds=%d changes=%d destroys=%d, want 3,1,2", adds, changes, destroys)
	}
}

func TestParseWarnings_RealFixture(t *testing.T) {
	// Captured verbatim from a real `terraform plan -no-color -out=tfplan`
	// run (v1.16.2) against a provider block with a deprecated inline
	// `version` constraint — not hand-typed, so this is the actual
	// format being parsed, divider included, rather than a guess at it.
	const output = `Terraform used the selected providers to generate the following execution
plan. Resource actions are indicated with the following symbols:
  + create

Terraform will perform the following actions:

  # null_resource.a will be created
  + resource "null_resource" "a" {
      + id = (known after apply)
    }

Plan: 1 to add, 0 to change, 0 to destroy.

Warning: Version constraints inside provider configuration blocks are deprecated

  on main.tf line 10, in provider "null":
  10:   version = "~> 3.0"

Terraform 0.13 and earlier allowed provider version constraints inside the
provider configuration block, but that is now deprecated and will be removed
in a future version of Terraform. To silence this warning, move the provider
version constraint into the required_providers block.

───────────────────────────────────────────────────────────────────────────

Saved the plan to: tfplan

To perform exactly these actions, run the following command to apply:
    terraform apply "tfplan"
`
	got := parseWarnings(output)
	if len(got) != 1 {
		t.Fatalf("got %d warnings, want 1: %+v", len(got), got)
	}
	w := got[0]
	if w.Summary != "Version constraints inside provider configuration blocks are deprecated" {
		t.Errorf("Summary = %q", w.Summary)
	}
	wantDetail := `  on main.tf line 10, in provider "null":
  10:   version = "~> 3.0"

Terraform 0.13 and earlier allowed provider version constraints inside the
provider configuration block, but that is now deprecated and will be removed
in a future version of Terraform. To silence this warning, move the provider
version constraint into the required_providers block.`
	if w.Detail != wantDetail {
		t.Errorf("Detail = %q, want %q", w.Detail, wantDetail)
	}
}

func TestParseWarnings_MultipleBackToBackAndTrailing(t *testing.T) {
	// Two warnings with no divider between them (terminated by the next
	// diagnostic header instead) and no trailing content after the
	// second one at all (terminated by end of string).
	const output = `Warning: Argument is deprecated

  on main.tf line 5, in resource "aws_instance" "foo":
   5:   foo = "bar"

This attribute is deprecated and will be removed in a future release.

Warning: Value for undeclared variable

A variable named "unused" was assigned on the command line, but the root
module does not declare a variable of that name.`
	got := parseWarnings(output)
	if len(got) != 2 {
		t.Fatalf("got %d warnings, want 2: %+v", len(got), got)
	}
	if got[0].Summary != "Argument is deprecated" {
		t.Errorf("warning 0 Summary = %q", got[0].Summary)
	}
	if !strings.Contains(got[0].Detail, "aws_instance") || !strings.Contains(got[0].Detail, "deprecated and will be removed") {
		t.Errorf("warning 0 Detail missing expected content: %q", got[0].Detail)
	}
	if got[1].Summary != "Value for undeclared variable" {
		t.Errorf("warning 1 Summary = %q", got[1].Summary)
	}
	if !strings.Contains(got[1].Detail, `does not declare a variable`) {
		t.Errorf("warning 1 Detail missing expected content: %q", got[1].Detail)
	}
}

func TestParseWarnings_StopsAtError(t *testing.T) {
	// A warning immediately followed by an error diagnostic: the
	// warning's detail must not swallow the error's text, and the error
	// itself must not come back as a Warning.
	const output = `Warning: Argument is deprecated

This attribute is deprecated.

Error: Invalid value

This value is invalid.
`
	got := parseWarnings(output)
	if len(got) != 1 {
		t.Fatalf("got %d warnings, want 1: %+v", len(got), got)
	}
	if got[0].Detail != "This attribute is deprecated." {
		t.Errorf("Detail = %q, want it to stop before the Error diagnostic", got[0].Detail)
	}
}

func TestParseWarnings_None(t *testing.T) {
	if got := parseWarnings("Plan: 2 to add, 0 to change, 0 to destroy.\n"); got != nil {
		t.Errorf("got %+v, want nil", got)
	}
}

func TestParsePlanSummaryLine_NoMatchReturnsZeros(t *testing.T) {
	adds, changes, destroys := parsePlanSummaryLine("no plan summary in this text at all")
	if adds != 0 || changes != 0 || destroys != 0 {
		t.Errorf("got adds=%d changes=%d destroys=%d, want 0,0,0", adds, changes, destroys)
	}
}

func TestParsePlanSummaryLine_EmptyInput(t *testing.T) {
	adds, changes, destroys := parsePlanSummaryLine("")
	if adds != 0 || changes != 0 || destroys != 0 {
		t.Errorf("got adds=%d changes=%d destroys=%d, want 0,0,0", adds, changes, destroys)
	}
}
