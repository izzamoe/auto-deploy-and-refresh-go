package store

import (
	"encoding/base64"
	"errors"
	"testing"
)

// T050 (FR-028, B55): creating an app with its env vars and args is ONE transaction.

func TestCreateWithConfig_StoresAppEnvAndArgsTogether(t *testing.T) {
	f := newDeleteFixture(t)
	app, err := f.apps.CreateWithConfig(CreateAppInput{
		Name: "full", Secret: "s3", BinaryPath: "/opt/f/f", ServiceName: "f.service", GithubRepo: "o/f", ArtifactName: "f",
		EnvVars: []EnvVar{{Name: "PORT", Value: "8080"}, {Name: "TOKEN", Value: "x"}},
		Args:    []string{"--port", "8080"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := f.envs.Get(app.ID); len(got) != 2 || got[0].Name != "PORT" {
		t.Errorf("env = %v", got)
	}
	if got, _ := f.args.Get(app.ID); len(got) != 2 || got[1] != "8080" {
		t.Errorf("args = %v", got)
	}
	if found, err := f.apps.GetBySecretHash(HashSecret("s3")); err != nil || found == nil || found.ID != app.ID {
		t.Errorf("the new secret must authenticate the new app: %v %v", found, err)
	}
}

func TestCreateWithConfig_InvalidEnvOrArgsCreatesNothing(t *testing.T) {
	f := newDeleteFixture(t)
	base := CreateAppInput{Name: "bad", Secret: "s4", BinaryPath: "/opt/b/b", ServiceName: "b.service", GithubRepo: "o/b", ArtifactName: "b"}

	badEnv := base
	badEnv.EnvVars = []EnvVar{{Name: "OK", Value: "1"}, {Name: "1BAD", Value: "x"}}
	badArgs := base
	badArgs.Args = []string{"fine", "bad\narg"}
	tooMany := base
	tooMany.Args = make([]string, MaxServiceArgs+1)

	for name, in := range map[string]CreateAppInput{"invalid env name": badEnv, "control character in an arg": badArgs, "too many args": tooMany} {
		if _, err := f.apps.CreateWithConfig(in); !errors.Is(err, ErrInvalidAppConfig) {
			t.Errorf("%s: err = %v, want ErrInvalidAppConfig", name, err)
		}
	}
	apps, _ := f.apps.List()
	if len(apps) != 0 {
		t.Errorf("%d apps exist after invalid creates, want none", len(apps))
	}
	if found, _ := f.apps.GetBySecretHash(HashSecret("s4")); found != nil {
		t.Error("a refused create must not leave a usable webhook secret behind")
	}
}

// A failure of the LAST insert rolls back the app row and the earlier child row.
func TestCreateWithConfig_RollsBackWhenAChildInsertFails(t *testing.T) {
	f := newDeleteFixture(t)
	if _, err := f.db.Exec(`CREATE TRIGGER fail_args_insert BEFORE INSERT ON app_args BEGIN SELECT RAISE(ABORT, 'args insert failed'); END`); err != nil {
		t.Fatal(err)
	}
	_, err := f.apps.CreateWithConfig(CreateAppInput{
		Name: "rb", Secret: "s5", BinaryPath: "/opt/r/r", ServiceName: "r.service", GithubRepo: "o/r", ArtifactName: "r",
		EnvVars: []EnvVar{{Name: "A", Value: "1"}}, Args: []string{"x"},
	})
	if err == nil {
		t.Fatal("expected the child insert failure to surface")
	}
	var apps, envs int
	f.db.QueryRow(`SELECT COUNT(*) FROM apps`).Scan(&apps)
	f.db.QueryRow(`SELECT COUNT(*) FROM app_env`).Scan(&envs)
	if apps != 0 || envs != 0 {
		t.Errorf("after a failed create: %d apps, %d env rows; want 0 and 0 (transaction must roll back)", apps, envs)
	}
	if found, _ := f.apps.GetBySecretHash(HashSecret("s5")); found != nil {
		t.Error("the secret-hash cache must not know the rolled-back app")
	}
}

func TestCreateWithConfig_DuplicateIsErrDuplicateAppAndAtomic(t *testing.T) {
	f := newDeleteFixture(t)
	in := CreateAppInput{Name: "d", Secret: "s6", BinaryPath: "/opt/d/d", ServiceName: "d.service", GithubRepo: "o/d", ArtifactName: "d", EnvVars: []EnvVar{{Name: "A", Value: "1"}}}
	if _, err := f.apps.CreateWithConfig(in); err != nil {
		t.Fatal(err)
	}
	in.Name = "d2"
	if _, err := f.apps.CreateWithConfig(in); !errors.Is(err, ErrDuplicateApp) {
		t.Fatalf("duplicate create = %v, want ErrDuplicateApp", err)
	}
	var envs int
	f.db.QueryRow(`SELECT COUNT(*) FROM app_env`).Scan(&envs)
	if envs != 1 {
		t.Errorf("%d env rows, want 1: the refused duplicate must not write env", envs)
	}
}

func TestCreateWithConfig_WorksWithoutEnvArgsTablesAndWithoutConfig(t *testing.T) {
	s := newTestAppStore(t) // env/args stores never constructed
	if _, err := s.CreateWithConfig(CreateAppInput{Name: "p", Secret: "s7", BinaryPath: "/opt/p/p", ServiceName: "p.service", GithubRepo: "o/p", ArtifactName: "p"}); err != nil {
		t.Fatalf("plain create: %v", err)
	}
	if _, err := s.CreateWithConfig(CreateAppInput{Name: "q", Secret: "s8", BinaryPath: "/opt/q/q", ServiceName: "q.service", GithubRepo: "o/q", ArtifactName: "q", Args: []string{"x"}}); err != nil {
		t.Fatalf("create with args but no pre-built args store: %v", err)
	}
}

func TestGenerateWebhookSecret_Is32RandomBytes(t *testing.T) {
	a, err := GenerateWebhookSecret()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := GenerateWebhookSecret()
	raw, err := base64.RawURLEncoding.DecodeString(a)
	if err != nil || len(raw) != 32 {
		t.Fatalf("secret %q decodes to %d bytes (%v), want 32", a, len(raw), err)
	}
	if len(a) != 43 || a == b {
		t.Errorf("secret length %d, two secrets equal = %v; want 43 characters and unique", len(a), a == b)
	}
}
