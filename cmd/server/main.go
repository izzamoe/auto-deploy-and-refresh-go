package main

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"os/signal"
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
	"github.com/izzamoe/auto-deploy/internal/progress"
	"github.com/izzamoe/auto-deploy/internal/store"
)

type response struct {
	Status string `json:"status"`
	Tag    string `json:"tag,omitempty"`
	Error  string `json:"error,omitempty"`
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
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
	coord := coordinator.NewCoordinator(appStore, q, func(app *store.App, jobID, tag string) (store.DownloadSummary, error) {
		return deploy.DeployWithControl(app, jobID, tag, tracker, dlClient, cancelService)
	}, tracker)
	coord.Start(ctx)
	adminHandler, err := admin.NewAdminHandler(serviceCfg)
	if err != nil {
		slog.Error("admin handler init failed", "err", err)
		db.Close()
		os.Exit(1)
	}
	templates := adminHandler.Templates()
	progressAdminHandler := admin.NewProgressAdminHandler(tracker, appStore, q, templates)
	adminAPIHandler := admin.NewAdminAPIHandler(appStore, q, tracker, cancelService)
	loginHandler, err := admin.NewLoginHandler(serviceCfg, jwtHandler)
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
	auth := admin.HertzSessionAuthMiddleware(serviceCfg, jwtHandler)
	admin.RegisterLoginRoutesHertz(h, loginHandler)
	h.POST("/webhook", multiAppWebhookHandler(admissionSvc))
	admin.RegisterAdminEventRoutesHertz(h, adminEventHub, auth)
	admin.RegisterAdminAPIRoutesHertz(h, adminAPIHandler, auth)
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
			c.JSON(consts.StatusBadRequest, response{Status: "error", Error: "missing or empty tag"})
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
