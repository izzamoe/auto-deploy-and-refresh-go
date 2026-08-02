package main

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	_ "net/http/pprof" // registers /debug/pprof/* on the DefaultServeMux
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/hlog"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/izzamoe/auto-deploy/internal/admin"
	"github.com/izzamoe/auto-deploy/internal/admission"
	"github.com/izzamoe/auto-deploy/internal/cancel"
	"github.com/izzamoe/auto-deploy/internal/config"
	"github.com/izzamoe/auto-deploy/internal/coordinator"
	"github.com/izzamoe/auto-deploy/internal/deploy"
	"github.com/izzamoe/auto-deploy/internal/download"
	"github.com/izzamoe/auto-deploy/internal/github"
	"github.com/izzamoe/auto-deploy/internal/llmstxt"
	"github.com/izzamoe/auto-deploy/internal/progress"
	"github.com/izzamoe/auto-deploy/internal/store"
)

type response struct {
	Status string `json:"status"`
	Tag    string `json:"tag,omitempty"`
	Error  string `json:"error,omitempty"`
}

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
	jwtHandler, err := admin.NewJWTHandler()
	if err != nil {
		slog.Error("jwt handler init failed", "err", err)
		os.Exit(1)
	}
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
	if seeded, err := adminUsers.EnsureSeed("admin", "11"); err != nil {
		slog.Error("admin user seed failed", "err", err)
		db.Close()
		os.Exit(1)
	} else if seeded {
		slog.Warn("seeded default admin account admin/11 — change the password immediately via the admin UI")
	}
	if err := q.RecoverStale(); err != nil {
		slog.Warn("stale recovery", "err", err)
	}
	if _, err := appStore.BootstrapIfEmpty(config.LoadLegacyBootstrapConfig()); err != nil {
		slog.Error("bootstrap failed", "err", err)
		db.Close()
		os.Exit(1)
	}
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
		return deploy.DeployArtifact(app, jobID, tag, tracker, dlClient, cancelService, githubClient)
	}, tracker)
	coord.SetLogCapturer(deploy.CaptureServiceLogsSince)
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
	accountHandler := admin.NewAccountHandler(adminUsers, jwtHandler, serviceCfg)
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
	loginHandler, err := admin.NewLoginHandler(serviceCfg, jwtHandler, adminUsers)
	if err != nil {
		slog.Error("login handler init failed", "err", err)
		db.Close()
		os.Exit(1)
	}
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
	admin.SetupMiddleware(h)
	auth := admin.HertzSessionAuthMiddleware(jwtHandler, adminUsers)
	admin.RegisterLoginRoutesHertz(h, loginHandler)
	h.POST("/webhook", multiAppWebhookHandler(admissionSvc))
	llmstxt.RegisterRoutesHertz(h)
	admin.RegisterAdminEventRoutesHertz(h, adminEventHub, auth)
	admin.RegisterAccountRoutesHertz(h, accountHandler, auth)
	admin.RegisterTelegramRoutesHertz(h, telegramHandler, auth)
	admin.RegisterGitHubRoutesHertz(h, githubConfigHandler, auth)
	admin.RegisterAppEnvRoutesHertz(h, appEnvHandler, auth)
	admin.RegisterAppArgsRoutesHertz(h, appArgsHandler, auth)
	admin.RegisterAdminAPIRoutesHertz(h, adminAPIHandler, auth)
	admin.RegisterReleasesRoutesHertz(h, releasesHandler, auth)
	admin.RegisterServiceUnitRoutesHertz(h, serviceUnitHandler, auth)
	admin.RegisterSystemRoutesHertz(h, systemLogsHandler, auth)
	admin.RegisterAdminProgressRoutesHertz(h, progressAdminHandler, auth)
	admin.RegisterAdminSPARoutesHertz(h, auth)
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

func multiAppWebhookHandler(admissionSvc *admission.AdmissionService) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		authHeader := string(c.GetHeader("Authorization"))
		if !strings.HasPrefix(authHeader, "Bearer ") {
			c.JSON(consts.StatusUnauthorized, response{Status: "error", Error: "unauthorized"})
			return
		}
		var body struct {
			Tag string `json:"tag"`
		}
		if err := c.BindJSON(&body); err != nil || body.Tag == "" {
			c.JSON(consts.StatusBadRequest, response{Status: "error", Error: "missing or empty tag"})
			return
		}
		result := admissionSvc.Admit(strings.TrimPrefix(authHeader, "Bearer "), body.Tag)
		switch result.Outcome {
		case admission.OutcomeUnauthorized:
			c.JSON(consts.StatusUnauthorized, response{Status: "error", Error: "unauthorized"})
		case admission.OutcomeBadRequest:
			errMsg := "invalid tag"
			if result.Tag == "" {
				errMsg = "missing or empty tag"
			}
			c.JSON(consts.StatusBadRequest, response{Status: "error", Error: errMsg})
		case admission.OutcomeDuplicate:
			c.JSON(consts.StatusOK, response{Status: "duplicate", Tag: body.Tag})
		case admission.OutcomeQueued:
			c.JSON(consts.StatusAccepted, response{Status: "queued", Tag: body.Tag})
		case admission.OutcomeError:
			errMsg := "internal error"
			if result.Error != nil {
				errMsg = result.Error.Error()
			}
			if errMsg == "queue full" {
				c.JSON(consts.StatusServiceUnavailable, response{Status: "error", Error: "queue full"})
			} else {
				c.JSON(consts.StatusInternalServerError, response{Status: "error", Error: errMsg})
			}
		default:
			c.JSON(consts.StatusInternalServerError, response{Status: "error", Error: "unexpected outcome"})
		}
	}
}
