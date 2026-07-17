package store

import (
	"runtime"
	"testing"
)

// TestSecretHashCache_RebuildInvalidateRace drives the exact interleaving that
// makes a lazily-rebuilt cache go permanently stale:
//
//	reader misses  → queries DB (sees OLD secret)
//	writer rotates → commits + invalidates
//	reader stores  → OLD snapshot overwrites the invalidation ← the bug
//
// With event-based invalidation and no serialization between rebuild and
// invalidate, the reader's Store races the writer's Store(nil). If the stale
// value wins, GetBySecretHash keeps authenticating the OLD secret (and
// rejecting the new one) until the next unrelated write — a silent auth bug.
//
// The testHookAfterQuery seam pauses the reader after it has read the old row
// but before it publishes, so the writer's rotation+invalidation is guaranteed
// to be in flight at the moment of publication. Looping exercises the window
// many times; the fix must make every iteration observe the NEW secret.
func TestSecretHashCache_RebuildInvalidateRace(t *testing.T) {
	const iterations = 500

	store := newTestAppStore(t)
	app, err := store.Create("app", "secret-v1", "/bin/app", "app.service", "o/r", "art")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	stale := 0
	for i := range iterations {
		oldSecret := "secret-v" + itoa(i+1)
		newSecret := "secret-v" + itoa(i+2)
		oldHash := HashSecret(oldSecret)
		newHash := HashSecret(newSecret)

		// Warm the cache so this iteration starts from a hit, then invalidate so
		// the GetBySecretHash below takes the rebuild path we want to race.
		if _, err := store.GetBySecretHash(oldHash); err != nil {
			t.Fatalf("warm: %v", err)
		}
		store.invalidateSecretHashCache()

		committed := make(chan struct{})
		done := make(chan struct{})
		store.testHookAfterQuery = func() {
			store.testHookAfterQuery = nil // fire once; reader has read the OLD row
			// Concurrent writer, split into its two real sub-steps so the test
			// controls the interleaving: commit the new secret (raw UPDATE, the
			// same write RotateSecret issues) THEN invalidate. Splitting them is
			// required — RotateSecret bundles both, and its invalidate would
			// block on the rebuild lock while this hook still holds the reader,
			// deadlocking. invalidateSecretHashCache below is the real
			// production method under test.
			go func() {
				defer close(done)
				if _, err := store.db.Exec(
					`UPDATE apps SET webhook_secret_hash = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
					newHash, app.ID,
				); err != nil {
					t.Errorf("writer commit: %v", err)
				}
				close(committed)
				store.invalidateSecretHashCache()
			}()
			<-committed       // ensure the new secret is committed to the DB…
			runtime.Gosched() // …and give the invalidation a chance to land first
		}

		if _, err := store.GetBySecretHash(oldHash); err != nil {
			t.Fatalf("raced read: %v", err)
		}
		<-done

		// After a committed rotation the cache must reflect the NEW secret.
		got, err := store.GetBySecretHash(newHash)
		if err != nil {
			t.Fatalf("post-read new: %v", err)
		}
		if got == nil {
			stale++
		}
	}

	if stale > 0 {
		t.Fatalf("cache served a stale snapshot in %d/%d iterations "+
			"(new secret not visible after a committed rotation)", stale, iterations)
	}
}

// itoa is a tiny dependency-free int→string for building distinct secrets.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[pos:])
}
