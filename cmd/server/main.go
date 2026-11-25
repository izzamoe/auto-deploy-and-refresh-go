package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	_ "net/http/pprof" // registers /debug/pprof/* on the DefaultServeMux
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/hlog"
	"github.com/izzamoe/auto-deploy/internal/admin"
	"github.com/izzamoe/auto-deploy/internal/admission"
	"github.com/izzamoe/auto-deploy/internal/auth"
	"github.com/izzamoe/auto-deploy/internal/cancel"
	"github.com/izzamoe/auto-deploy/internal/config"
	"github.com/izzamoe/auto-deploy/internal/coordinator"
	"github.com/izzamoe/auto-deploy/internal/deploy"
	"github.com/izzamoe/auto-deploy/internal/download"
	"github.com/izzamoe/auto-deploy/internal/github"
	"github.com/izzamoe/auto-deploy/internal/llmstxt"
	"github.com/izzamoe/auto-deploy/internal/progress"
	"github.com/izzamoe/auto-deploy/internal/store"
	"github.com/izzamoe/auto-deploy/internal/webhook"
)

func main() {
	// Subcommands run and exit before the server boots. "upgrade"/"update"
	// re-run the install script to replace this binary in place.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "upgrade", "update":
			os.Exit(runUpgrade(os.Args[2:]))
		}
	}

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	// Optional profiling endpoint. Off unless PPROF_ADDR is set; bind to a
	// loopback address (e.g. "localhost:6060") so it is never exposed publicly.
	if pprofAddr := os.Getenv("PPROF_ADDR"); pprofAddr != "" {
		go func() {
			slog.Info("pprof listening", "addr", pprofAddr)
			srv := &http.Server{Addr: pprofAddr, ReadHeaderTimeout: 5 * time.Second}
			if err := srv.ListenAndServe(); err != nil {
				slog.Error("pprof server error", "err", err)
			}
		}()
	}
	hertzLogger, err := admin.NewZapLogger()
	if err != nil {
		slog.Error("zap logger init failed", "err", err)
		os.Exit(1)
	}
	hlog.SetLogger(hertzLogger)
	serviceCfg, err := config.LoadServiceConfig()
	if err != nil {
		slog.Error(err.Error())
		os.Exit(1)
	}
	db, err := sql.Open("sqlite", serviceCfg.QueueDBPath)
	if err != nil {
		slog.Error("db open failed", "err", err)
		os.Exit(1)
	}
	db.SetMaxOpenConns(1)
	q, err := store.NewDeployQueue(db, serviceCfg.QueueMax)
	if err != nil {
		slog.Error("queue init failed", "err", err)
		db.Close()
		os.Exit(1)
	}
	if err := q.Migrate(); err != nil {
		slog.Error("queue migrate failed", "err", err)
		db.Close()
		os.Exit(1)
	}
	defer q.Close()
	appStore, err := store.NewAppStore(db)
	if err != nil {
		slog.Error("app store init failed", "err", err)
		db.Close()
		os.Exit(1)
	}
	// AppStore.Delete removes deploy_jobs rows directly; keep the queue's
	// IsDuplicate cache coherent with that out-of-band write.
	appStore.SetJobsDeletedHook(q.InvalidateActiveJobsCache)
	adminUsers, err := store.NewAdminUserStore(db)
	if err != nil {
		slog.Error("admin user store init failed", "err", err)
		db.Close()
		os.Exit(1)
	}
	// Versioned, expand-only schema migrations. They ALTER apps/admin_users/
	// deploy_jobs, so they run after the stores (and q.Migrate) created those tables.
	if err := store.Migrate(db); err != nil {
		slog.Error("schema migrate failed", "err", err)
		db.Close()
		os.Exit(1)
	}
	// First start: create the admin account with a random one-time password in an
	// owner-only file next to the database (Q-3). Only the path is logged.
	if created, passwordPath, err := adminUsers.EnsureInitialAdmin(filepath.Dir(serviceCfg.QueueDBPath)); err != nil {
		slog.Error("initial admin account setup failed", "err", err)
		db.Close()
		os.Exit(1)
	} else if created {
		slog.Warn("created the initial admin account: read its one-time password from the file, sign in, and change it (the file is deleted after the change)",
			"username", store.InitialAdminUsername, "password_file", passwordPath)
	}
	warnRetiredEnv()
	// FR-044/FR-144: settle a self-deploy first (the new process finding its pending
	// file means the new binary started), then fail whatever else a dead process left.
	stateDir := filepath.Dir(serviceCfg.QueueDBPath)
	deploy.ConfigureSelfDeploy(serviceCfg.SelfServiceName, stateDir)
	if _, err := deploy.RecoverSelfDeploy(stateDir, queueSelfDeployResolver{q}); err != nil {
		slog.Warn("self-deploy recovery", "err", err)
	}
	if err := q.RecoverStale(); err != nil {
		slog.Warn("stale recovery", "err", err)
	}
	if _, err := appStore.BootstrapIfEmpty(config.LoadLegacyBootstrapConfig()); err != nil {
		slog.Error("bootstrap failed", "err", err)
		db.Close()
		os.Exit(1)
	}
	// FR-044: an interrupted deploy leaves a missing binary or a transaction marker.
	if apps, err := appStore.List(); err == nil {
		paths := make([]string, 0, len(apps))
		for _, a := range apps {
			paths = append(paths, a.BinaryPath)
		}
		deploy.RecoverInterrupted(paths)
	}
	// FR-040 backstop: from here on the store itself refuses an app whose binaryPath,
	// serviceName, githubRepo or artifactName is unsafe (the API validates first). It is
	// installed AFTER the legacy bootstrap above so an existing single-app install keeps
	// starting.
	appStore.SetInputValidator(func(binaryPath, serviceName, githubRepo, artifactName string) error {
		if errs := deploy.ValidateAppInput(deploy.AppInput{BinaryPath: binaryPath, ServiceName: serviceName, GithubRepo: githubRepo, ArtifactName: artifactName}); len(errs) > 0 {
			return fmt.Errorf("%s: %s", errs[0].Field, errs[0].Message)
		}
		return nil
	})
	admissionSvc := admission.NewAdmissionService(appStore, q)
	tracker := progress.NewProgressTracker()
	dlClient := download.NewDownloadClient(serviceCfg.DownloadDNS)
	adminEventHub := admin.NewAdminEventHub(tracker)
	tracker.SetProgressSink(adminEventHub)
	cancelService := cancel.NewCancelService(q)
	cancelService.SetEventSink(adminEventHub)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	githubClient := github.NewClient(serviceCfg.GitHubToken)
	coord := coordinator.NewCoordinator(appStore, q, func(app *store.App, jobID, tag string) (store.DownloadSummary, error) {
		return deploy.DeployArtifactContext(ctx, app, jobID, tag, tracker, dlClient, cancelService, githubClient)
	}, tracker)
	coord.SetLogCapturer(deploy.CaptureServiceLogsSince)
	coord.SetEventSink(adminEventHub) // job_status after every terminal MarkDone (events-ws.md 3.4)
	coord.Start(ctx)
	telegramConfigStore, err := store.NewTelegramConfigStore(db)
	if err != nil {
		slog.Error("telegram config store init failed", "err", err)
		db.Close()
		os.Exit(1)
	}
	telegramSessionPath := filepath.Join(filepath.Dir(serviceCfg.QueueDBPath), "telegram-session.json")
	telegramHandler := admin.NewTelegramConfigHandler(ctx, telegramConfigStore, telegramSessionPath, coord)
	if err := telegramHandler.Reload(); err != nil {
		slog.Warn("telegram config reload failed", "err", err)
	}
	adminHandler, err := admin.NewAdminHandler(serviceCfg)
	if err != nil {
		slog.Error("admin handler init failed", "err", err)
		db.Close()
		os.Exit(1)
	}
	templates := adminHandler.Templates()
	progressAdminHandler := admin.NewProgressAdminHandler(tracker, appStore, q, templates)
	adminAPIHandler := admin.NewAdminAPIHandler(appStore, q, tracker, cancelService)
	// Sessions (FR-003..FR-006): persistent signing key, server-side session rows,
	// token_version revocation. Tokens: hashed API tokens for automation (FR-014).
	sessionKey, regenerated, err := auth.LoadOrCreateKey(serviceCfg.SessionKeyPath)
	if err != nil {
		slog.Error("session key init failed", "err", err)
		db.Close()
		os.Exit(1)
	}
	if regenerated {
		slog.Warn("session key was missing or corrupt and has been regenerated; every existing session is invalid", "path", serviceCfg.SessionKeyPath)
	}
	adminSessions, err := store.NewAdminSessionStore(db)
	if err != nil {
		slog.Error("admin session store init failed", "err", err)
		db.Close()
		os.Exit(1)
	}
	go cleanSessions(ctx, adminSessions)
	apiTokens, err := store.NewAPITokenStore(db)
	if err != nil {
		slog.Error("api token store init failed", "err", err)
		db.Close()
		os.Exit(1)
	}
	sessionMgr, err := auth.NewManager(auth.SessionConfig{Key: sessionKey, Secure: serviceCfg.CookieSecure}, adminUsers, adminSessions)
	if err != nil {
		slog.Error("session manager init failed", "err", err)
		db.Close()
		os.Exit(1)
	}
	adminEventHub.SetSessionChecker(sessionMgr) // 4401 when a socket's session ends (B12, FR-025)
	if !serviceCfg.CookieSecure {
		slog.Warn("COOKIE_SECURE=false: the session cookie is sent over plain HTTP; use this only for local development")
	}
	clientIPs := auth.NewClientIPResolver(serviceCfg.TrustedProxyCIDRs)
	authMW := auth.Middleware(auth.MiddlewareConfig{
		Sessions:     sessionMgr,
		Tokens:       apiTokens,
		TokenLimiter: auth.NewWindow(auth.TokenFailureMax, auth.TokenFailureWindow, nil),
		ClientIP:     clientIPs,
	})
	sessionHandler := admin.NewSessionHandler(admin.SessionHandlerConfig{
		Sessions: sessionMgr,
		Users:    adminUsers,
		Limiter:  auth.NewLoginLimiter(nil),
		ClientIP: clientIPs,
	})
	tokensHandler := admin.NewTokensHandler(apiTokens)
	accountHandler := admin.NewAccountHandler(adminUsers, sessionMgr, filepath.Join(filepath.Dir(serviceCfg.QueueDBPath), store.InitialAdminPasswordFile))
	releasesHandler := admin.NewReleasesHandler(appStore, githubClient)
	githubConfigStore, err := store.NewGitHubConfigStore(db)
	if err != nil {
		slog.Error("github config store init failed", "err", err)
		db.Close()
		os.Exit(1)
	}
	githubConfigHandler := admin.NewGitHubConfigHandler(githubConfigStore, githubClient, serviceCfg.GitHubToken)
	if err := githubConfigHandler.Reload(); err != nil {
		slog.Warn("github config reload failed", "err", err)
	}
	appEnvStore, err := store.NewAppEnvStore(db)
	if err != nil {
		slog.Error("app env store init failed", "err", err)
		db.Close()
		os.Exit(1)
	}
	appEnvHandler := admin.NewAppEnvHandler(appStore, appEnvStore)
	appArgsStore, err := store.NewAppArgsStore(db)
	if err != nil {
		slog.Error("app args store init failed", "err", err)
		db.Close()
		os.Exit(1)
	}
	appArgsHandler := admin.NewAppArgsHandler(appStore, appArgsStore)
	serviceUnitHandler := admin.NewServiceUnitAdminHandler(appStore, appEnvStore, appArgsStore)
	systemLogsHandler := admin.NewSystemLogsHandler(serviceCfg.SelfServiceName)
	h := server.New(
		server.WithHostPorts(serviceCfg.ListenAddr),
		server.WithReadTimeout(45*time.Second),
		server.WithWriteTimeout(45*time.Second),
		server.WithIdleTimeout(90*time.Second),
		server.WithKeepAliveTimeout(time.Minute),
		server.WithMaxRequestBodySize(16*1024*1024),
		server.WithMaxHeaderBytes(1<<20),
		server.WithMaxKeepBodySize(4*1024*1024),
		server.WithReadBufferSize(4*1024),
		server.WithKeepAlive(true),
		server.WithExitWaitTime(30*time.Second),
		server.WithNetwork("tcp"),
	)
	admin.SetupMiddlewareWithOptions(h, admin.SecurityHeadersConfig{HSTS: serviceCfg.CookieSecure})
	// publicPage lets the static admin shell load without a session (Q-17: it holds no
	// data; every data endpoint and the event socket sit behind authMW).
	publicPage := func(ctx context.Context, c *app.RequestContext) { c.Next(ctx) }
	admin.RegisterSessionRoutesHertz(h, sessionHandler, authMW)
	admin.RegisterTokenRoutesHertz(h, tokensHandler, authMW)
	h.POST("/webhook", webhook.HandlerWithLimits(admissionSvc, webhook.Limits{ClientIP: clientIPs}))
	llmstxt.RegisterRoutesHertz(h)
	admin.RegisterAdminEventRoutesHertz(h, adminEventHub, authMW)
	admin.RegisterAccountRoutesHertz(h, accountHandler, authMW)
	admin.RegisterTelegramRoutesHertz(h, telegramHandler, authMW)
	admin.RegisterGitHubRoutesHertz(h, githubConfigHandler, authMW)
	admin.RegisterAppEnvRoutesHertz(h, appEnvHandler, authMW)
	admin.RegisterAppArgsRoutesHertz(h, appArgsHandler, authMW)
	admin.RegisterAdminAPIRoutesHertz(h, adminAPIHandler, authMW)
	admin.RegisterReleasesRoutesHertz(h, releasesHandler, authMW)
	admin.RegisterServiceUnitRoutesHertz(h, serviceUnitHandler, authMW)
	admin.RegisterSystemRoutesHertz(h, systemLogsHandler, authMW)
	admin.RegisterAdminProgressRoutesHertz(h, progressAdminHandler, authMW)
	// Pre-v1 paths stay for ONE release as deprecated aliases of the v1 routes (Q-7);
	// register them after every v1 route.
	admin.RegisterLegacyAPIAliases(h, authMW)
	admin.RegisterAdminSPARoutesHertz(h, publicPage)
	go func() {
		slog.Info("starting", "addr", serviceCfg.ListenAddr)
		if err := h.Run(); err != nil {
			slog.Error("server error", "err", err)
			os.Exit(1)
		}
	}()
	<-ctx.Done()
	slog.Info("shutting down")
	coord.Wait()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := h.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown error", "err", err)
	}
}

// sessionRetention is how long expired or revoked session rows are kept.
const sessionRetention = 7 * 24 * time.Hour

// cleanSessions deletes stale session rows at start and then every hour.
func cleanSessions(ctx context.Context, sessions *store.AdminSessionStore) {
	run := func() {
		n, err := sessions.DeleteStale(time.Now().Add(-sessionRetention))
		if err != nil {
			slog.Warn("session cleanup failed", "err", err)
		} else if n > 0 {
			slog.Info("deleted stale admin sessions", "count", n)
		}
	}
	run()
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			run()
		}
	}
}

// queueSelfDeployResolver settles a self-deploy job in the queue.
type queueSelfDeployResolver struct{ q *store.DeployQueue }

func (r queueSelfDeployResolver) MarkSelfDeploySucceeded(jobID string) error {
	return r.q.MarkDone(jobID, true, "", nil)
}

func (r queueSelfDeployResolver) MarkSelfDeployRolledBack(jobID, message string) error {
	if err := r.q.MarkDone(jobID, false, message, nil); err != nil {
		return err
	}
	return r.q.MarkRolledBack(jobID)
}
