package queries

import (
	"strings"
	"testing"
)

func TestSyncJobQueriesIncludeWaitingJobs(t *testing.T) {
	for name, query := range map[string]string{"ClaimJob": ClaimJob, "NextWake": NextWake} {
		if !strings.Contains(query, "WAITING") {
			t.Fatalf("%s query does not include WAITING: %s", name, query)
		}
	}
}

func TestSyncJobQueriesResetWaitingStateOnCompletion(t *testing.T) {
	if !strings.Contains(MarkSynced, "waiting_since = NULL") {
		t.Fatalf("MarkSynced does not clear waiting_since: %s", MarkSynced)
	}
	if !strings.Contains(MarkFailed, "waiting_since") {
		t.Fatalf("MarkFailed does not persist waiting_since: %s", MarkFailed)
	}
}
