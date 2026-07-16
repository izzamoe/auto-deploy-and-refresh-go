package store

import (
	"database/sql"
	"errors"
	"strconv"
	"sync"
	"testing"
)

// getBySecretHashUncached replicates the pre-cache implementation of
// GetBySecretHash (one SQLite SELECT per call). It is the benchmark baseline
// that the in-memory cache is measured against.
func getBySecretHashUncached(db *sql.DB, hash string) (*App, error) {
	row := db.QueryRow(
		`SELECT id, name, webhook_secret_hash, binary_path, service_name, github_repo, artifact_name, enabled, created_at, updated_at
		 FROM apps WHERE webhook_secret_hash = ? AND enabled = 1`,
		hash,
	)
	app, err := scanAppRow(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &app, nil
}

// ---------------------------------------------------------------------------
// Correctness: cache serves reads and every writer invalidates it.
// ---------------------------------------------------------------------------

func mustGetBySecretHash(t *testing.T, store *AppStore, secret string) *App {
	t.Helper()
	app, err := store.GetBySecretHash(HashSecret(secret))
	if err != nil {
		t.Fatalf("GetBySecretHash(%q): %v", secret, err)
	}
	return app
}

func TestGetBySecretHashServedFromCache(t *testing.T) {
	t.Parallel()
	store := newTestAppStore(t)
	if _, err := store.Create("a", "sec", "/bin/a", "a.service", "o/a", "art"); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Cache is empty before the first read.
	if store.secretHashCache.Load() != nil {
		t.Fatal("cache should be nil before first read")
	}
	if got := mustGetBySecretHash(t, store, "sec"); got == nil || got.Name != "a" {
		t.Fatalf("first read = %+v, want app 'a'", got)
	}
	// After the first read the snapshot is populated; a second read is served
	// from it (no DB touch).
	if store.secretHashCache.Load() == nil {
		t.Fatal("cache should be populated after first read")
	}
	if got := mustGetBySecretHash(t, store, "sec"); got == nil || got.Name != "a" {
		t.Fatalf("second read = %+v, want app 'a'", got)
	}
}

func TestGetBySecretHashCacheInvalidatedOnCreate(t *testing.T) {
	t.Parallel()
	store := newTestAppStore(t)
	if _, err := store.Create("a", "sec-a", "/bin/a", "a.service", "o/a", "art-a"); err != nil {
		t.Fatalf("Create a: %v", err)
	}
	// Warm the cache with only app 'a' present.
	mustGetBySecretHash(t, store, "sec-a")

	// Create a second app; a stale cache would not contain it.
	if _, err := store.Create("b", "sec-b", "/bin/b", "b.service", "o/b", "art-b"); err != nil {
		t.Fatalf("Create b: %v", err)
	}
	if got := mustGetBySecretHash(t, store, "sec-b"); got == nil || got.Name != "b" {
		t.Fatalf("after Create, GetBySecretHash(b) = %+v, want app 'b' (cache not invalidated?)", got)
	}
}

func TestGetBySecretHashCacheInvalidatedOnSetEnabled(t *testing.T) {
	t.Parallel()
	store := newTestAppStore(t)
	app, err := store.Create("a", "sec", "/bin/a", "a.service", "o/a", "art")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	mustGetBySecretHash(t, store, "sec") // warm

	if err := store.SetEnabled(app.ID, false); err != nil {
		t.Fatalf("SetEnabled(false): %v", err)
	}
	if got := mustGetBySecretHash(t, store, "sec"); got != nil {
		t.Fatalf("disabled app still returned: %+v", got)
	}

	if err := store.SetEnabled(app.ID, true); err != nil {
		t.Fatalf("SetEnabled(true): %v", err)
	}
	if got := mustGetBySecretHash(t, store, "sec"); got == nil {
		t.Fatal("re-enabled app not returned")
	}
}

func TestGetBySecretHashCacheInvalidatedOnRotateSecret(t *testing.T) {
	t.Parallel()
	store := newTestAppStore(t)
	app, err := store.Create("a", "old-sec", "/bin/a", "a.service", "o/a", "art")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	mustGetBySecretHash(t, store, "old-sec") // warm

	if err := store.RotateSecret(app.ID, "new-sec"); err != nil {
		t.Fatalf("RotateSecret: %v", err)
	}
	if got := mustGetBySecretHash(t, store, "old-sec"); got != nil {
		t.Fatalf("old secret still resolves after rotate: %+v", got)
	}
	if got := mustGetBySecretHash(t, store, "new-sec"); got == nil || got.ID != app.ID {
		t.Fatalf("new secret does not resolve after rotate: %+v", got)
	}
}

func TestGetBySecretHashCacheInvalidatedOnUpdateSecret(t *testing.T) {
	t.Parallel()
	store := newTestAppStore(t)
	app, err := store.Create("a", "old-sec", "/bin/a", "a.service", "o/a", "art")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	mustGetBySecretHash(t, store, "old-sec") // warm

	if err := store.UpdateWithOptionalSecret(app.ID, "a", "new-sec", "/bin/a", "a.service", "o/a", "art", true); err != nil {
		t.Fatalf("UpdateWithOptionalSecret: %v", err)
	}
	if got := mustGetBySecretHash(t, store, "old-sec"); got != nil {
		t.Fatalf("old secret still resolves after update: %+v", got)
	}
	if got := mustGetBySecretHash(t, store, "new-sec"); got == nil {
		t.Fatal("new secret does not resolve after update")
	}
}

func TestGetBySecretHashCacheInvalidatedOnUpdateDisable(t *testing.T) {
	t.Parallel()
	store := newTestAppStore(t)
	app, err := store.Create("a", "sec", "/bin/a", "a.service", "o/a", "art")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	mustGetBySecretHash(t, store, "sec") // warm

	// Update keeps the secret but flips enabled to false.
	if err := store.Update(app.ID, "a", "/bin/a", "a.service", "o/a", "art", false); err != nil {
		t.Fatalf("Update(disable): %v", err)
	}
	if got := mustGetBySecretHash(t, store, "sec"); got != nil {
		t.Fatalf("app disabled via Update still returned: %+v", got)
	}
}

func TestGetBySecretHashCacheInvalidatedOnDelete(t *testing.T) {
	t.Parallel()
	store := newTestAppStoreWithJobs(t)
	app, err := store.Create("a", "sec", "/bin/a", "a.service", "o/a", "art")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	mustGetBySecretHash(t, store, "sec") // warm

	if err := store.Delete(app.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got := mustGetBySecretHash(t, store, "sec"); got != nil {
		t.Fatalf("deleted app still resolves: %+v", got)
	}
}

func TestGetBySecretHashDisabledNeverReturned(t *testing.T) {
	t.Parallel()
	store := newTestAppStore(t)
	app, err := store.Create("a", "sec", "/bin/a", "a.service", "o/a", "art")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.SetEnabled(app.ID, false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	// First read after disabling — cache built fresh must exclude it.
	if got := mustGetBySecretHash(t, store, "sec"); got != nil {
		t.Fatalf("disabled app returned on fresh cache: %+v", got)
	}
}

// The value returned by GetBySecretHash must be a copy: mutating it must not
// corrupt the shared in-memory snapshot.
func TestGetBySecretHashReturnsIsolatedCopy(t *testing.T) {
	t.Parallel()
	store := newTestAppStore(t)
	if _, err := store.Create("a", "sec", "/bin/a", "a.service", "o/a", "art"); err != nil {
		t.Fatalf("Create: %v", err)
	}

	first := mustGetBySecretHash(t, store, "sec")
	first.Name = "MUTATED"
	first.BinaryPath = "/tmp/hacked"

	second := mustGetBySecretHash(t, store, "sec")
	if second.Name != "a" || second.BinaryPath != "/bin/a" {
		t.Fatalf("cache corrupted by caller mutation: %+v", second)
	}
}

func TestGetBySecretHashUnknownReturnsNil(t *testing.T) {
	t.Parallel()
	store := newTestAppStore(t)
	if _, err := store.Create("a", "sec", "/bin/a", "a.service", "o/a", "art"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := store.GetBySecretHash(HashSecret("does-not-exist"))
	if err != nil {
		t.Fatalf("GetBySecretHash(unknown): %v", err)
	}
	if got != nil {
		t.Fatalf("unknown hash returned %+v, want nil", got)
	}
}

// Race detector coverage: concurrent readers against a writer that keeps
// invalidating the snapshot must not corrupt state (run with -race).
func TestGetBySecretHashConcurrentReadWrite(t *testing.T) {
	t.Parallel()
	store := newTestAppStore(t)
	app, err := store.Create("a", "sec", "/bin/a", "a.service", "o/a", "art")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	hash := HashSecret("sec")

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for range 8 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
					if _, err := store.GetBySecretHash(hash); err != nil {
						t.Errorf("concurrent read: %v", err)
						return
					}
				}
			}
		})
	}
	for i := range 200 {
		if err := store.SetEnabled(app.ID, i%2 == 0); err != nil {
			t.Errorf("SetEnabled: %v", err)
			break
		}
	}
	close(stop)
	wg.Wait()
}

// ---------------------------------------------------------------------------
// Benchmarks: cached GetBySecretHash vs the original per-call SQLite query.
// ---------------------------------------------------------------------------

func seedApps(b *testing.B, store *AppStore, n int) (validHash string) {
	b.Helper()
	for i := range n {
		s := "secret-" + strconv.Itoa(i)
		_, err := store.Create(
			"app-"+strconv.Itoa(i), s,
			"/bin/app-"+strconv.Itoa(i),
			"app-"+strconv.Itoa(i)+".service",
			"owner/app-"+strconv.Itoa(i),
			"artifact-"+strconv.Itoa(i),
		)
		if err != nil {
			b.Fatalf("seed Create %d: %v", i, err)
		}
	}
	return HashSecret("secret-0")
}

func newBenchAppStore(b *testing.B) *AppStore {
	b.Helper()
	dbPath := b.TempDir() + "/bench.db"
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		b.Fatalf("open db: %v", err)
	}
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		b.Fatalf("WAL: %v", err)
	}
	db.SetMaxOpenConns(1)
	b.Cleanup(func() { db.Close() })
	store, err := NewAppStore(db)
	if err != nil {
		b.Fatalf("NewAppStore: %v", err)
	}
	return store
}

func benchGetBySecretHashCached(b *testing.B, n int) {
	store := newBenchAppStore(b)
	hash := seedApps(b, store, n)
	if _, err := store.GetBySecretHash(hash); err != nil { // warm cache
		b.Fatalf("warm: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		app, err := store.GetBySecretHash(hash)
		if err != nil || app == nil {
			b.Fatalf("GetBySecretHash = %+v, %v", app, err)
		}
	}
}

func benchGetBySecretHashUncached(b *testing.B, n int) {
	store := newBenchAppStore(b)
	hash := seedApps(b, store, n)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		app, err := getBySecretHashUncached(store.db, hash)
		if err != nil || app == nil {
			b.Fatalf("uncached = %+v, %v", app, err)
		}
	}
}

func BenchmarkGetBySecretHash_Uncached_N10(b *testing.B) { benchGetBySecretHashUncached(b, 10) }
func BenchmarkGetBySecretHash_Cached_N10(b *testing.B)   { benchGetBySecretHashCached(b, 10) }
func BenchmarkGetBySecretHash_Uncached_N50(b *testing.B) { benchGetBySecretHashUncached(b, 50) }
func BenchmarkGetBySecretHash_Cached_N50(b *testing.B)   { benchGetBySecretHashCached(b, 50) }
