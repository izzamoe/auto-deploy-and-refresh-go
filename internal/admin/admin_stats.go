package admin

import (
	"context"
	"regexp"
	"strings"
	"time"
	_ "time/tzdata" // IANA zones for ?tz= even on hosts without /usr/share/zoneinfo

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/izzamoe/auto-deploy/internal/httpx"
	"github.com/izzamoe/auto-deploy/internal/store"
)

// GET /admin/api/v1/stats?range=7d|30d&tz=<IANA name> (FR-026, Q-8): the data of the
// dashboard. Day buckets follow the requested zone (default UTC).

// tzNameRe is the shape of an IANA zone name ("Asia/Jakarta", "America/Argentina/
// Buenos_Aires", "Etc/GMT+7"); anything else is rejected before it reaches the
// loader, so a request can never name a file path.
var tzNameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_+\-]*(/[A-Za-z0-9_+\-]+){0,2}$`)

type adminAPIStatsResponse struct {
	GeneratedAt time.Time `json:"generatedAt"`
	Range       string    `json:"range"`
	TZ          string    `json:"tz"`
	Apps        struct {
		Total   int `json:"total"`
		Enabled int `json:"enabled"`
	} `json:"apps"`
	Deploys24h struct {
		Total     int   `json:"total"`
		Succeeded int   `json:"succeeded"`
		Failed    int   `json:"failed"`
		Canceled  int   `json:"canceled"`
		Hourly    []int `json:"hourly"`
	} `json:"deploys24h"`
	Active struct {
		Running int `json:"running"`
		Queued  int `json:"queued"`
	} `json:"active"`
	SuccessRate struct {
		Succeeded    int      `json:"succeeded"`
		Total        int      `json:"total"`
		Rate         *float64 `json:"rate"`
		PreviousRate *float64 `json:"previousRate"`
	} `json:"successRate"`
	Daily     []adminAPIDailyBucket `json:"daily"`
	Rollbacks struct {
		Count int `json:"count"`
	} `json:"rollbacks"`
	Recent []adminAPIJobResponse `json:"recent"`
}

type adminAPIDailyBucket struct {
	Date      string `json:"date"`
	Succeeded int    `json:"succeeded"`
	Failed    int    `json:"failed"`
	Canceled  int    `json:"canceled"`
}

func (h *AdminAPIHandler) GetStatsHertz(ctx context.Context, c *app.RequestContext) {
	var errs []httpx.FieldError
	rangeParam := string(c.Query("range"))
	days := 7
	switch rangeParam {
	case "", "7d":
		rangeParam = "7d"
	case "30d":
		days = 30
	default:
		errs = append(errs, httpx.FieldError{Field: "range", Code: httpx.CodeValidationFailed, Message: "range must be 7d or 30d"})
	}
	tzParam := string(c.Query("tz"))
	loc := time.UTC
	if tzParam != "" {
		l, err := parseStatsZone(tzParam)
		if err != nil {
			errs = append(errs, httpx.FieldError{Field: "tz", Code: httpx.CodeValidationFailed, Message: "tz must be an IANA time zone name such as UTC or Asia/Jakarta"})
		} else {
			loc = l
		}
	}
	if len(errs) > 0 {
		writeAdminAPIFieldErrorsHertz(c, errs)
		return
	}

	st, err := h.queue.Stats(h.now(), days, loc)
	if err != nil {
		logHandlerError(c, "stats failed", err)
		writeAdminAPIErrorHertz(c, consts.StatusInternalServerError, "Failed to compute statistics")
		return
	}

	resp := adminAPIStatsResponse{GeneratedAt: st.GeneratedAt, Range: rangeParam, TZ: st.TZ}
	resp.Apps.Total, resp.Apps.Enabled = st.Apps.Total, st.Apps.Enabled
	resp.Deploys24h.Total, resp.Deploys24h.Succeeded = st.Deploys24h.Total, st.Deploys24h.Succeeded
	resp.Deploys24h.Failed, resp.Deploys24h.Canceled = st.Deploys24h.Failed, st.Deploys24h.Canceled
	resp.Deploys24h.Hourly = st.Deploys24h.Hourly
	resp.Active.Running, resp.Active.Queued = st.Active.Running, st.Active.Queued
	resp.SuccessRate.Succeeded, resp.SuccessRate.Total = st.SuccessRate.Succeeded, st.SuccessRate.Total
	resp.SuccessRate.Rate, resp.SuccessRate.PreviousRate = st.SuccessRate.Rate, st.SuccessRate.PreviousRate
	resp.Rollbacks.Count = st.Rollbacks
	resp.Daily = make([]adminAPIDailyBucket, 0, len(st.Daily))
	for _, d := range st.Daily {
		resp.Daily = append(resp.Daily, adminAPIDailyBucket{Date: d.Date, Succeeded: d.Succeeded, Failed: d.Failed, Canceled: d.Canceled})
	}

	apps := map[string]store.App{}
	if all, err := h.store.List(); err == nil {
		for _, a := range all {
			apps[a.ID] = a
		}
	}
	resp.Recent = make([]adminAPIJobResponse, 0, len(st.Recent))
	for _, j := range st.Recent {
		var owner *store.App
		if a, ok := apps[j.AppID]; ok {
			owner = &a
		}
		jr := jobResponse(owner, j.JobRecord, h.tracker)
		rolledBack := j.RolledBack
		jr.RolledBack = &rolledBack
		resp.Recent = append(resp.Recent, jr)
	}
	c.JSON(consts.StatusOK, resp)
}

// parseStatsZone accepts an IANA zone name only: not "Local", not a path, not control
// characters.
func parseStatsZone(name string) (*time.Location, error) {
	if len(name) > 64 || !tzNameRe.MatchString(name) || strings.EqualFold(name, "Local") {
		return nil, errBadZone
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, errBadZone
	}
	return loc, nil
}

type statsZoneError struct{}

func (statsZoneError) Error() string { return "invalid time zone" }

var errBadZone error = statsZoneError{}
