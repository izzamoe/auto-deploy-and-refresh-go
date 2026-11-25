package deploy

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/izzamoe/auto-deploy/internal/progress"
)

// B18 / FR-043: replacing the binary must be atomic (the binary path never disappears,
// the backup is a hard link or a verified copy, files and directory are fsynced) and
// leave a transaction marker while the replacement is in flight.

type replaceProbe struct {
	t        *testing.T
	bin      string
	events   []string
	missing  []string // hooks at which the binary path did not exist
	sameFile bool     // link hook saw bin and bak as one inode (after the link)
}

func (p *replaceProbe) checkBin(where string) {
	if _, err := os.Stat(p.bin); err != nil {
		p.missing = append(p.missing, where)
	}
}

// installReplaceProbe hooks rename/link/sync and records what happens in order.
func installReplaceProbe(t *testing.T, bin string) *replaceProbe {
	p := &replaceProbe{t: t, bin: bin}
	origRename, origLink, origSyncFile, origSyncDir := renameFile, linkFile, syncFile, syncDir
	t.Cleanup(func() { renameFile, linkFile, syncFile, syncDir = origRename, origLink, origSyncFile, origSyncDir })

	renameFile = func(oldpath, newpath string) error {
		p.checkBin("rename " + filepath.Base(oldpath) + "->" + filepath.Base(newpath))
		p.events = append(p.events, "rename:"+filepath.Base(oldpath)+"->"+filepath.Base(newpath))
		return origRename(oldpath, newpath)
	}
	linkFile = func(oldpath, newpath string) error {
		p.checkBin("link " + filepath.Base(oldpath) + "->" + filepath.Base(newpath))
		p.events = append(p.events, "link:"+filepath.Base(oldpath)+"->"+filepath.Base(newpath))
		err := origLink(oldpath, newpath)
		if err == nil {
			a, _ := os.Stat(oldpath)
			b, _ := os.Stat(newpath)
			p.sameFile = a != nil && b != nil && os.SameFile(a, b)
		}
		return err
	}
	syncFile = func(path string) error {
		p.events = append(p.events, "syncfile:"+filepath.Base(path))
		return origSyncFile(path)
	}
	syncDir = func(dir string) error {
		p.events = append(p.events, "syncdir")
		return origSyncDir(dir)
	}
	return p
}

func healthyServiceStub(t *testing.T) {
	stubDeploySleep(t)
	stubSystemctl(t, func(name string, args ...string) ([]byte, error) {
		return []byte("active\n"), nil
	})
}

func (p *replaceProbe) index(prefix string) int {
	for i, e := range p.events {
		if strings.HasPrefix(e, prefix) {
			return i
		}
	}
	return -1
}

// B18: no hook point of a deploy may observe a missing binary path.
func TestReplace_BinaryPathNeverMissing(t *testing.T) {
	app := testApp(t.TempDir())
	if err := os.WriteFile(app.BinaryPath, elfBinary("old-binary"), 0755); err != nil {
		t.Fatal(err)
	}
	healthyServiceStub(t)
	p := installReplaceProbe(t, app.BinaryPath)

	if _, err := DeployArtifact(app, "job-b18-a", "v2", progress.NewProgressTracker(), deployClientWithBody(elfBinary("new-binary")), nil, nil); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if len(p.missing) > 0 {
		t.Errorf("B18: the binary path did not exist at %v (events %v)", p.missing, p.events)
	}
	assertFileBytes(t, app.BinaryPath, elfBinary("new-binary"))
}

// FR-043: the new file is fsynced BEFORE it is renamed over the binary, and the
// directory is fsynced AFTER the rename.
func TestReplace_FsyncsFileBeforeRenameAndDirAfter(t *testing.T) {
	app := testApp(t.TempDir())
	if err := os.WriteFile(app.BinaryPath, elfBinary("old-binary"), 0755); err != nil {
		t.Fatal(err)
	}
	healthyServiceStub(t)
	p := installReplaceProbe(t, app.BinaryPath)

	if _, err := DeployArtifact(app, "job-b18-b", "v2", progress.NewProgressTracker(), deployClientWithBody(elfBinary("new-binary")), nil, nil); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	syncTmp := p.index("syncfile:binary.tmp")
	rename := p.index("rename:binary.tmp->binary")
	if syncTmp < 0 || rename < 0 || syncTmp > rename {
		t.Fatalf("B18: the temp file must be fsynced before the rename; events = %v", p.events)
	}
	after := false
	for _, e := range p.events[rename+1:] {
		if e == "syncdir" {
			after = true
		}
	}
	if !after {
		t.Errorf("B18: the directory must be fsynced after the rename; events = %v", p.events)
	}
}

// FR-043: the backup is a hard link of the running binary (and replaces an older .bak).
func TestReplace_BackupIsAHardLink(t *testing.T) {
	app := testApp(t.TempDir())
	if err := os.WriteFile(app.BinaryPath, elfBinary("old-binary"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(app.BinaryPath+".bak", []byte("stale backup"), 0755); err != nil {
		t.Fatal(err)
	}
	healthyServiceStub(t)
	p := installReplaceProbe(t, app.BinaryPath)

	if _, err := DeployArtifact(app, "job-b18-c", "v2", progress.NewProgressTracker(), deployClientWithBody(elfBinary("new-binary")), nil, nil); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if !p.sameFile {
		t.Errorf("B18: the backup must be a hard link of the old binary; events = %v", p.events)
	}
	assertFileBytes(t, app.BinaryPath+".bak", elfBinary("old-binary"))
}

// FR-043: where hard links are impossible (EXDEV, EPERM, ...) the backup is a copy,
// fsynced, and the binary path still never disappears.
func TestReplace_BackupFallsBackToCopy(t *testing.T) {
	app := testApp(t.TempDir())
	original := elfBinary("old-binary")
	if err := os.WriteFile(app.BinaryPath, original, 0755); err != nil {
		t.Fatal(err)
	}
	healthyServiceStub(t)
	p := installReplaceProbe(t, app.BinaryPath)
	linkFile = func(string, string) error { return &os.LinkError{Op: "link", Err: syscall.EXDEV} }

	if _, err := DeployArtifact(app, "job-b18-d", "v2", progress.NewProgressTracker(), deployClientWithBody(elfBinary("new-binary")), nil, nil); err != nil {
		t.Fatalf("deploy with copy fallback: %v", err)
	}
	assertFileBytes(t, app.BinaryPath+".bak", original)
	if len(p.missing) > 0 {
		t.Errorf("the binary path disappeared at %v", p.missing)
	}
	if p.index("syncfile:binary.bak") < 0 && p.index("syncfile:binary.bak.tmp") < 0 {
		t.Errorf("the copied backup must be fsynced; events = %v", p.events)
	}
	if info, err := os.Stat(app.BinaryPath + ".bak"); err != nil || info.Mode().Perm()&0o100 == 0 {
		t.Errorf("the backup copy must stay executable: %v %v", info, err)
	}
}

// FR-043 / FR-044: <bin>.deploying exists while the replacement is in flight and is
// gone after a successful deploy and after a successful rollback; it stays when the
// rollback itself cannot restore the binary.
func TestReplace_MarkerLifecycle(t *testing.T) {
	t.Run("present during restart, removed on success", func(t *testing.T) {
		app := testApp(t.TempDir())
		if err := os.WriteFile(app.BinaryPath, elfBinary("old-binary"), 0755); err != nil {
			t.Fatal(err)
		}
		stubDeploySleep(t)
		var seen bool
		stubSystemctl(t, func(name string, args ...string) ([]byte, error) {
			if name == "restart" {
				_, err := os.Stat(deployingMarkerPath(app.BinaryPath))
				seen = err == nil
			}
			return []byte("active\n"), nil
		})
		if _, err := DeployArtifact(app, "job-b18-e", "v2", progress.NewProgressTracker(), deployClientWithBody(elfBinary("new-binary")), nil, nil); err != nil {
			t.Fatal(err)
		}
		if !seen {
			t.Error("the .deploying marker must exist while the service is restarted")
		}
		assertPathMissing(t, deployingMarkerPath(app.BinaryPath))
	})
	t.Run("removed after a successful rollback", func(t *testing.T) {
		app := testApp(t.TempDir())
		if err := os.WriteFile(app.BinaryPath, elfBinary("old-binary"), 0755); err != nil {
			t.Fatal(err)
		}
		stubDeploySleep(t)
		restarts := 0
		stubSystemctl(t, func(name string, args ...string) ([]byte, error) {
			if name == "restart" {
				restarts++
				if restarts == 1 {
					return []byte("boom"), errors.New("exit status 1")
				}
			}
			return []byte("active\n"), nil
		})
		_, err := DeployArtifact(app, "job-b18-f", "v2", progress.NewProgressTracker(), deployClientWithBody(elfBinary("new-binary")), nil, nil)
		if err == nil {
			t.Fatal("expected failure")
		}
		assertPathMissing(t, deployingMarkerPath(app.BinaryPath))
	})
	t.Run("kept when the rollback cannot restore the binary", func(t *testing.T) {
		app := testApp(t.TempDir())
		if err := os.WriteFile(app.BinaryPath, elfBinary("old-binary"), 0755); err != nil {
			t.Fatal(err)
		}
		stubDeploySleep(t)
		stubSystemctl(t, func(name string, args ...string) ([]byte, error) {
			if name == "restart" {
				return []byte("boom"), errors.New("exit status 1")
			}
			return []byte("active\n"), nil
		})
		orig := renameFile
		renameFile = func(oldpath, newpath string) error {
			if oldpath == app.BinaryPath+".bak" {
				return fmt.Errorf("rollback blocked")
			}
			return orig(oldpath, newpath)
		}
		t.Cleanup(func() { renameFile = orig })
		if _, err := DeployArtifact(app, "job-b18-g", "v2", progress.NewProgressTracker(), deployClientWithBody(elfBinary("new-binary")), nil, nil); err == nil {
			t.Fatal("expected failure")
		}
		if _, err := os.Stat(deployingMarkerPath(app.BinaryPath)); err != nil {
			t.Errorf("the marker must stay so startup recovery can finish the job: %v", err)
		}
	})
	t.Run("removed when the deploy fails before touching the binary", func(t *testing.T) {
		app := testApp(t.TempDir())
		if err := os.WriteFile(app.BinaryPath, elfBinary("old-binary"), 0755); err != nil {
			t.Fatal(err)
		}
		stubDeploySleep(t)
		stubSystemctl(t, func(string, ...string) ([]byte, error) { return []byte("active\n"), nil })
		stubChmod(t, func(string, os.FileMode) error { return errors.New("chmod denied") })
		if _, err := DeployArtifact(app, "job-b18-h", "v2", progress.NewProgressTracker(), deployClientWithBody(elfBinary("new-binary")), nil, nil); err == nil {
			t.Fatal("expected failure")
		}
		assertPathMissing(t, deployingMarkerPath(app.BinaryPath))
		assertFileBytes(t, app.BinaryPath, elfBinary("old-binary"))
	})
}
