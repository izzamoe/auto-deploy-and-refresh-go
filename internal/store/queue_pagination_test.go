package store

import (
	"fmt"
	"testing"
)

func TestListHistoryPagedAndCount(t *testing.T) {
	t.Parallel()
	q := newTestQueue(t, 100)

	// Enqueue 25 jobs for app1 and 3 for app2.
	for i := range 25 {
		if err := q.Enqueue("app1", fmt.Sprintf("v1.0.%d", i)); err != nil {
			t.Fatalf("Enqueue app1 #%d: %v", i, err)
		}
	}
	for i := range 3 {
		if err := q.Enqueue("app2", fmt.Sprintf("v2.0.%d", i)); err != nil {
			t.Fatalf("Enqueue app2 #%d: %v", i, err)
		}
	}

	total, err := q.CountHistory("app1")
	if err != nil {
		t.Fatalf("CountHistory: %v", err)
	}
	if total != 25 {
		t.Fatalf("CountHistory(app1) = %d, want 25", total)
	}

	// First page of 20.
	page1, err := q.ListHistoryPaged("app1", 20, 0)
	if err != nil {
		t.Fatalf("ListHistoryPaged page1: %v", err)
	}
	if len(page1) != 20 {
		t.Fatalf("page1 len = %d, want 20", len(page1))
	}
	// Newest first: the last enqueued tag (index 24) should lead.
	if page1[0].Tag != "v1.0.24" {
		t.Fatalf("page1[0].Tag = %q, want v1.0.24 (newest first)", page1[0].Tag)
	}

	// Second page holds the remaining 5.
	page2, err := q.ListHistoryPaged("app1", 20, 20)
	if err != nil {
		t.Fatalf("ListHistoryPaged page2: %v", err)
	}
	if len(page2) != 5 {
		t.Fatalf("page2 len = %d, want 5", len(page2))
	}

	// No overlap between pages.
	seen := make(map[string]bool)
	for _, j := range page1 {
		seen[j.ID] = true
	}
	for _, j := range page2 {
		if seen[j.ID] {
			t.Fatalf("job %s appears on both pages", j.ID)
		}
	}
}

func TestListAllHistoryPagedAndCount(t *testing.T) {
	t.Parallel()
	q := newTestQueue(t, 100)

	for i := range 10 {
		if err := q.Enqueue("app1", fmt.Sprintf("v1.0.%d", i)); err != nil {
			t.Fatalf("Enqueue app1 #%d: %v", i, err)
		}
	}
	for i := range 5 {
		if err := q.Enqueue("app2", fmt.Sprintf("v2.0.%d", i)); err != nil {
			t.Fatalf("Enqueue app2 #%d: %v", i, err)
		}
	}

	total, err := q.CountAllHistory()
	if err != nil {
		t.Fatalf("CountAllHistory: %v", err)
	}
	if total != 15 {
		t.Fatalf("CountAllHistory = %d, want 15", total)
	}

	page1, err := q.ListAllHistoryPaged(10, 0)
	if err != nil {
		t.Fatalf("ListAllHistoryPaged page1: %v", err)
	}
	if len(page1) != 10 {
		t.Fatalf("page1 len = %d, want 10", len(page1))
	}

	page2, err := q.ListAllHistoryPaged(10, 10)
	if err != nil {
		t.Fatalf("ListAllHistoryPaged page2: %v", err)
	}
	if len(page2) != 5 {
		t.Fatalf("page2 len = %d, want 5", len(page2))
	}
}
