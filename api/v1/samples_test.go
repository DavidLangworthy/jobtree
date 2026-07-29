package v1

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// TestShippedSamplesAreValid validates every manifest under config/samples
// through the same Validate() the webhook calls.
//
// THIS EXISTS BECAUSE NOTHING CHECKED THEM. The manifest corpus
// (validation_corpus_test.go) covers hand-written JSON fixtures, and the envtest
// webhook suite covers that same corpus against a real apiserver — but the YAML
// we actually SHIP as examples was validated by nothing at all. So when Ruling
// 10 deleted maxGPUHours, both budget samples kept setting it and CI stayed
// green through the whole change; a user running `kubectl apply -f` on them
// would have been the first to find out. INV-WINDOW-REQUIRED would have done the
// same thing a phase later.
//
// A sample that the API server would reject is not an example, it is a bug
// report with good syntax highlighting.
func TestShippedSamplesAreValid(t *testing.T) {
	root := filepath.Join("..", "..", "config", "samples")
	var found int

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || (!strings.HasSuffix(path, ".yaml") && !strings.HasSuffix(path, ".yml")) {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// Only Budgets carry the rules this guards; other kinds are validated by
		// their own paths and a decoding failure here would be a false alarm.
		var probe struct {
			Kind string `json:"kind"`
		}
		if err := yaml.Unmarshal(raw, &probe); err != nil || probe.Kind != "Budget" {
			return nil
		}
		found++

		var budget Budget
		if err := yaml.UnmarshalStrict(raw, &budget); err != nil {
			// Strict decoding catches the exact failure mode that started this:
			// a field the CRD no longer has. The apiserver prunes or rejects it;
			// either way the sample is lying about the API.
			t.Errorf("%s: does not decode against the current API (a removed or misspelled field?): %v",
				filepath.Base(path), err)
			return nil
		}
		if err := budget.ValidateCreate(); err != nil {
			t.Errorf("%s: shipped sample is not a valid Budget: %v", filepath.Base(path), err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	if found == 0 {
		t.Fatalf("no Budget samples found under %s; this guard is checking nothing", root)
	}
	t.Logf("validated %d shipped Budget sample(s)", found)
}
