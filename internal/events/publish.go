package events

import (
	"time"
)

// Publisher is what a producer of state changes needs (plan.md section 4): a mutation
// handler calls PublishEvent AFTER its change is committed, and RefreshService when a
// systemd unit may have changed state (control, unit apply, deploy). The admin event hub
// implements it; producers receive it through a SetEventPublisher setter, so their
// constructors do not change.
type Publisher interface {
	PublishEvent(Event)
	RefreshService(appID string)
}

// Event types of topic events (events-ws.v3.md 2.2).
const (
	TypeSummary          = "summary"
	TypeNotice           = "notice"
	TypeJobStatus        = "job_status"
	TypeCancelRequested  = "cancel_requested"
	TypeAppUpserted      = "app.upserted"
	TypeAppDeleted       = "app.deleted"
	TypeAppConfigChanged = "app.config_changed"
	TypeServiceStatus    = "service.status"
	TypeStatsChanged     = "stats.changed"
	TypeSettingsChanged  = "settings.changed"
	TypeAccountChanged   = "account.changed"
	TypeLogsLines        = "logs.lines"
	TypeLogsGap          = "logs.gap"
)

// Parts of app.config_changed.
const (
	PartEnv  = "env"
	PartArgs = "args"
	PartUnit = "unit"
)

// Sections of settings.changed and account.changed.
const (
	SectionGitHub   = "github"
	SectionTelegram = "telegram"
	SectionProfile  = "profile"
	SectionTokens   = "tokens"
	SectionSessions = "sessions"
)

// Reasons of stats.changed.
const (
	ReasonJob    = "job"
	ReasonApp    = "app"
	ReasonWindow = "window"
)

// OriginHeader is the optional request header that names the tab causing a mutation
// (contracts/admin-api.v1-revisi.md); its value becomes the event's origin.
const OriginHeader = "X-Admin-Client"

// MaxOriginLen bounds an origin (clientId) value.
const MaxOriginLen = 32

// ValidOrigin returns v when it is a valid clientId ([A-Za-z0-9_-]{1,32}) and "" otherwise:
// an invalid header is ignored, never an error.
func ValidOrigin(v string) string {
	if v == "" || len(v) > MaxOriginLen {
		return ""
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-':
		default:
			return ""
		}
	}
	return v
}

// AppPayload is the payload of app.upserted: the public App of the REST API (no secret, no
// liveProgress).
type AppPayload struct {
	App any `json:"app"`
}

// ConfigChangedPayload is the payload of app.config_changed (an invalidation: no content).
type ConfigChangedPayload struct {
	Part      string    `json:"part"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// SectionPayload is the payload of settings.changed and account.changed.
type SectionPayload struct {
	Section string `json:"section"`
}

// StatsChangedPayload is the payload of stats.changed.
type StatsChangedPayload struct {
	Reasons []string `json:"reasons"`
}

// ServiceStatusPayload is the payload of service.status.
type ServiceStatusPayload struct {
	State string `json:"state"`
}

// emptyPayload serializes as {} (app.deleted).
type emptyPayload struct{}

// AppUpserted announces the new public state of an app (topic apps).
func AppUpserted(appID string, app any) Event {
	return Event{Topic: TopicApps, Type: TypeAppUpserted, AppID: appID, Payload: AppPayload{App: app}}
}

// AppDeleted announces that an app is gone (topic apps).
func AppDeleted(appID string) Event {
	return Event{Topic: TopicApps, Type: TypeAppDeleted, AppID: appID, Payload: emptyPayload{}}
}

// AppConfigChanged invalidates one part of an app's configuration (env, args, unit) without
// carrying it (FR-005).
func AppConfigChanged(appID, part string, at time.Time) Event {
	return Event{Topic: TopicApps, Type: TypeAppConfigChanged, AppID: appID, Payload: ConfigChangedPayload{Part: part, UpdatedAt: at.UTC()}}
}

// StatsChanged invalidates the dashboard statistics (topic stats).
func StatsChanged(reasons ...string) Event {
	return Event{Topic: TopicStats, Type: TypeStatsChanged, Payload: StatsChangedPayload{Reasons: reasons}}
}

// SettingsChanged invalidates one settings section (topic settings).
func SettingsChanged(section string) Event {
	return Event{Topic: TopicSettings, Type: TypeSettingsChanged, Payload: SectionPayload{Section: section}}
}

// AccountChanged invalidates one section of a user's account; only that user's sockets
// receive it (FR-017).
func AccountChanged(userID, section string) Event {
	return Event{Topic: TopicAccount, Type: TypeAccountChanged, UserID: userID, Payload: SectionPayload{Section: section}}
}

// ServiceStatus announces the systemd state of an app's service (topic service).
func ServiceStatus(appID, state string) Event {
	return Event{Topic: TopicService, Type: TypeServiceStatus, AppID: appID, Payload: ServiceStatusPayload{State: state}}
}
