package v1

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/davidlangworthy/jobtree/internal/manifestcorpus"
)

// The accept/reject corpus (internal/manifestcorpus) exercises every
// validation rule from raw JSON manifests. This test runs it against the
// in-process methods; the envtest suite (controllers/kube) replays the same
// corpus through a real API server with the webhooks installed.

func TestRunManifestValidationCorpus(t *testing.T) {
	for _, tc := range manifestcorpus.Runs {
		t.Run(tc.Name, func(t *testing.T) {
			var run Run
			if err := json.Unmarshal([]byte(tc.Manifest), &run); err != nil {
				t.Fatalf("manifest does not parse: %v", err)
			}
			run.Default()
			err := run.ValidateCreate()
			checkValidation(t, err, tc.WantErr)
		})
	}
}

func TestBudgetManifestValidationCorpus(t *testing.T) {
	for _, tc := range manifestcorpus.Budgets {
		t.Run(tc.Name, func(t *testing.T) {
			var budget Budget
			if err := json.Unmarshal([]byte(tc.Manifest), &budget); err != nil {
				t.Fatalf("manifest does not parse: %v", err)
			}
			err := budget.ValidateCreate()
			checkValidation(t, err, tc.WantErr)
		})
	}
}

func checkValidation(t *testing.T, err error, wantErr string) {
	t.Helper()
	if wantErr == "" {
		if err != nil {
			t.Fatalf("expected manifest to be accepted, got: %v", err)
		}
		return
	}
	if err == nil {
		t.Fatalf("expected rejection containing %q, but manifest was accepted", wantErr)
	}
	if !strings.Contains(err.Error(), wantErr) {
		t.Fatalf("expected error containing %q, got: %v", wantErr, err)
	}
}
