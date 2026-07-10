package admin

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"testing"
	"time"

	"github.com/izzamoe/auto-deploy/internal/progress"
	"github.com/izzamoe/auto-deploy/internal/store"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/coder/websocket"
)

func makeProgressHertzServer(t *testing.T, tracker *progress.ProgressTracker, addr string) *server.Hertz {
	t.Helper()
	tmpls := make(map[string]*template.Template)
	for _, page := range []string{"apps_list.html", "history.html"} {
		tmpl, err := template.ParseFS(templateFS, "templates/base.html", "templates/"+page)
		if err != nil {
			t.Fatalf("parse %s: %v", page, err)
		}
		tmpls[page] = tmpl
	}
	appStore := newTestAppStoreWithJobs(t)
	queue, err := store.NewDeployQueue(appStore.DB(), 10)
	if err != nil {
		t.Fatalf("NewDeployQueue: %v", err)
	}
	if err := queue.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	h := server.New(server.WithHostPorts(addr), server.WithDisablePrintRoute(true))
	handler := NewProgressAdminHandler(tracker, appStore, queue, tmpls)
	RegisterAdminProgressRoutesHertz(h, handler, testHertzSessionAuth(t))
	return h
}

func TestOldProgressRouteNotRegistered(t *testing.T) {
	t.Parallel()
	tracker := progress.NewProgressTracker()
	engine := makeProgressHertzServer(t, tracker, "127.0.0.1:0")

	rec := ut.PerformRequest(engine.Engine, "GET", "/admin/progress/"+"stream", nil, ut.Header{Key: "Cookie", Value: testAdminSessionCookie(t)})

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected old progress route to be absent with 404, got %d", rec.Code)
	}
}

func TestProgressWebSocketRequiresAuth(t *testing.T) {
	t.Parallel()
	tracker := progress.NewProgressTracker()
	url := startProgressHertzServer(t, tracker, 19001)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, resp, err := websocket.Dial(ctx, url+"/admin/progress/ws", nil)
	if err == nil {
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		t.Fatal("expected unauthorized websocket dial to fail")
	}
	if resp == nil {
		t.Fatal("expected unauthorized response")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound && resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected unauthorized websocket handshake to fail via redirect or missing redirected login route, got %d", resp.StatusCode)
	}
}

func TestProgressWebSocketRejectsWrongPassword(t *testing.T) {
	t.Parallel()
	tracker := progress.NewProgressTracker()
	url := startProgressHertzServer(t, tracker, 19002)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	header := http.Header{}
	header.Set("Cookie", jwtCookieName+"=wrong")

	_, resp, err := websocket.Dial(ctx, url+"/admin/progress/ws", &websocket.DialOptions{HTTPHeader: header})
	if err == nil {
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		t.Fatal("expected websocket dial with wrong password to fail")
	}
	if resp == nil {
		t.Fatal("expected unauthorized response")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound && resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected unauthorized websocket handshake to fail via redirect or missing redirected login route, got %d", resp.StatusCode)
	}
}

func TestProgressWebSocketSendsAuthorizedCurrentState(t *testing.T) {
	t.Parallel()
	tracker := progress.NewProgressTracker()
	tracker.Start("app-current", "job-current", "v1.2.3")
	tracker.Update("app-current", 512, 1024, 123.9)
	url := startProgressHertzServer(t, tracker, 19003)

	conn := dialProgressWebSocket(t, url, "/admin/progress/ws")
	defer conn.Close(websocket.StatusNormalClosure, "")

	frame := readProgressWebSocketFrame(t, conn)
	if frame.AppID != "app-current" || frame.JobID != "job-current" || frame.Tag != "v1.2.3" {
		t.Fatalf("unexpected identity frame: %+v", frame)
	}
	if frame.Stage != progress.ProgressStageDownloading || frame.Status != progress.ProgressStatusInProgress {
		t.Fatalf("unexpected stage/status: %+v", frame)
	}
	if frame.Percent != 50 || frame.DownloadedBytes != 512 || frame.TotalBytes != 1024 || frame.SpeedBPS != 123 {
		t.Fatalf("unexpected progress values: %+v", frame)
	}
}

func TestProgressWebSocketFiltersCurrentState(t *testing.T) {
	t.Parallel()
	tracker := progress.NewProgressTracker()
	tracker.Start("app-one", "job-one", "v1.0.0")
	tracker.Update("app-one", 100, 1000, 10)
	tracker.Start("app-two", "job-two", "v2.0.0")
	tracker.Update("app-two", 250, 500, 20)
	url := startProgressHertzServer(t, tracker, 19004)

	conn := dialProgressWebSocket(t, url, "/admin/progress/ws?app_id=app-two&job_id=job-two")
	defer conn.Close(websocket.StatusNormalClosure, "")

	frame := readProgressWebSocketFrame(t, conn)
	if frame.AppID != "app-two" || frame.JobID != "job-two" {
		t.Fatalf("expected app-two/job-two frame, got %+v", frame)
	}
	if frame.Percent != 50 || frame.SpeedBPS != 20 {
		t.Fatalf("unexpected filtered frame values: %+v", frame)
	}
}

func TestProgressWebSocketSupportsMultipleSimultaneousClients(t *testing.T) {
	t.Parallel()
	tracker := progress.NewProgressTracker()
	tracker.Start("app-shared", "job-shared", "v3.0.0")
	tracker.Update("app-shared", 100, 1000, 100)
	url := startProgressHertzServer(t, tracker, 19005)

	connA := dialProgressWebSocket(t, url, "/admin/progress/ws?app_id=app-shared")
	defer connA.Close(websocket.StatusNormalClosure, "")
	connB := dialProgressWebSocket(t, url, "/admin/progress/ws?job_id=job-shared")
	defer connB.Close(websocket.StatusNormalClosure, "")

	if frame := readProgressWebSocketFrame(t, connA); frame.Percent != 10 {
		t.Fatalf("client A initial percent = %d, want 10", frame.Percent)
	}
	if frame := readProgressWebSocketFrame(t, connB); frame.Percent != 10 {
		t.Fatalf("client B initial percent = %d, want 10", frame.Percent)
	}

	tracker.Update("app-shared", 400, 1000, 200)

	if frame := readProgressWebSocketFrame(t, connA); frame.Percent != 40 || frame.SpeedBPS != 200 {
		t.Fatalf("client A update frame = %+v, want percent 40 speed 200", frame)
	}
	if frame := readProgressWebSocketFrame(t, connB); frame.Percent != 40 || frame.SpeedBPS != 200 {
		t.Fatalf("client B update frame = %+v, want percent 40 speed 200", frame)
	}
}

func TestProgressWebSocketReconnectReceivesCurrentState(t *testing.T) {
	t.Parallel()
	tracker := progress.NewProgressTracker()
	tracker.Start("app-reconnect", "job-reconnect", "v4.0.0")
	tracker.Update("app-reconnect", 100, 1000, 100)
	url := startProgressHertzServer(t, tracker, 19006)

	conn := dialProgressWebSocket(t, url, "/admin/progress/ws?app_id=app-reconnect")
	if frame := readProgressWebSocketFrame(t, conn); frame.Percent != 10 {
		t.Fatalf("initial percent = %d, want 10", frame.Percent)
	}
	tracker.Update("app-reconnect", 900, 1000, 300)
	if frame := readProgressWebSocketFrame(t, conn); frame.Percent != 90 || frame.SpeedBPS != 300 {
		t.Fatalf("update frame = %+v, want percent 90 speed 300", frame)
	}
	if err := conn.Close(websocket.StatusNormalClosure, "reconnect"); err != nil {
		t.Fatalf("close websocket: %v", err)
	}

	reconnected := dialProgressWebSocket(t, url, "/admin/progress/ws?app_id=app-reconnect")
	defer reconnected.Close(websocket.StatusNormalClosure, "")
	frame := readProgressWebSocketFrame(t, reconnected)
	if frame.AppID != "app-reconnect" || frame.JobID != "job-reconnect" || frame.Percent != 90 || frame.SpeedBPS != 300 {
		t.Fatalf("reconnected current state = %+v, want latest app-reconnect/job-reconnect percent 90 speed 300", frame)
	}
}

func TestProgressWebSocketTerminalFrameIsIdempotent(t *testing.T) {
	t.Parallel()
	tracker := progress.NewProgressTracker()
	tracker.Start("app-terminal", "job-terminal", "v5.0.0")
	tracker.Update("app-terminal", 1000, 1000, 500)
	url := startProgressHertzServer(t, tracker, 19007)

	conn := dialProgressWebSocket(t, url, "/admin/progress/ws?job_id=job-terminal")
	defer conn.Close(websocket.StatusNormalClosure, "")
	if frame := readProgressWebSocketFrame(t, conn); frame.Status != progress.ProgressStatusInProgress || frame.Percent != 100 {
		t.Fatalf("initial frame = %+v, want in-progress 100%%", frame)
	}

	tracker.Finish("app-terminal")
	terminal := readProgressWebSocketFrame(t, conn)
	if terminal.Stage != progress.ProgressStageSucceeded || terminal.Status != progress.ProgressStatusSucceeded {
		t.Fatalf("terminal frame = %+v, want succeeded", terminal)
	}

	tracker.Finish("app-terminal")
	expectNoProgressWebSocketFrame(t, conn, 750*time.Millisecond)
}

func startProgressHertzServer(t *testing.T, tracker *progress.ProgressTracker, port int) string {
	t.Helper()
	h := makeProgressHertzServer(t, tracker, fmt.Sprintf("127.0.0.1:%d", port))
	go h.Spin()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = h.Shutdown(ctx)
	})
	time.Sleep(100 * time.Millisecond)
	return fmt.Sprintf("ws://127.0.0.1:%d", port)
}

func dialProgressWebSocket(t *testing.T, baseURL, path string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	header := http.Header{}
	header.Set("Cookie", testAdminSessionCookie(t))
	conn, resp, err := websocket.Dial(ctx, baseURL+path, &websocket.DialOptions{HTTPHeader: header})
	if resp != nil && resp.Body != nil {
		defer resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("websocket dial failed: %v", err)
	}
	return conn
}

func readProgressWebSocketFrame(t *testing.T, conn *websocket.Conn) progress.ProgressFrame {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	messageType, payload, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("websocket read failed: %v", err)
	}
	if messageType != websocket.MessageText {
		t.Fatalf("expected text websocket message, got %v", messageType)
	}
	frame, err := progress.DecodeProgressFrame(string(payload))
	if err != nil {
		t.Fatalf("decode progress frame: %v", err)
	}
	return frame
}

func expectNoProgressWebSocketFrame(t *testing.T, conn *websocket.Conn, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_, payload, err := conn.Read(ctx)
	if err == nil {
		frame, decodeErr := progress.DecodeProgressFrame(string(payload))
		if decodeErr == nil {
			t.Fatalf("unexpected websocket progress frame: %+v", frame)
		}
		t.Fatalf("unexpected websocket payload: %q", string(payload))
	}
	if ctx.Err() == nil {
		t.Fatalf("websocket read failed before timeout: %v", err)
	}
}

func makeProgressHertzEngine(t *testing.T, tracker *progress.ProgressTracker) *server.Hertz {
	t.Helper()
	tmpls := make(map[string]*template.Template)
	for _, page := range []string{"apps_list.html", "history.html"} {
		tmpl, err := template.ParseFS(templateFS, "templates/base.html", "templates/"+page)
		if err != nil {
			t.Fatalf("parse %s: %v", page, err)
		}
		tmpls[page] = tmpl
	}
	appStore := newTestAppStoreWithJobs(t)
	queue, err := store.NewDeployQueue(appStore.DB(), 10)
	if err != nil {
		t.Fatalf("NewDeployQueue: %v", err)
	}
	if err := queue.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	h := server.New(server.WithHostPorts("127.0.0.1:0"))
	handler := NewProgressAdminHandler(tracker, appStore, queue, tmpls)
	auth := testHertzSessionAuth(t)
	RegisterAdminProgressRoutesHertz(h, handler, auth)
	return h
}

func TestRegisterAdminProgressRoutesHertz_RequiresAuth(t *testing.T) {
	t.Parallel()
	tracker := progress.NewProgressTracker()
	engine := makeProgressHertzEngine(t, tracker)

	w := ut.PerformRequest(engine.Engine, "GET", "/admin/progress/ws", nil)
	if w.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d", w.Code)
	}
	if got := w.Header().Get("Location"); got != "/admin/login" {
		t.Fatalf("expected login redirect, got %q", got)
	}
}

func TestRegisterAdminProgressRoutesHertz_RejectsWrongPassword(t *testing.T) {
	t.Parallel()
	tracker := progress.NewProgressTracker()
	engine := makeProgressHertzEngine(t, tracker)

	w := ut.PerformRequest(engine.Engine, "GET", "/admin/progress/ws", nil, ut.Header{Key: "Cookie", Value: jwtCookieName + "=wrong"})
	if w.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d", w.Code)
	}
}

func TestRegisterAdminProgressRoutesHertz_AcceptsAuth(t *testing.T) {
	t.Parallel()
	tracker := progress.NewProgressTracker()
	tracker.Start("app-hertz", "job-hertz", "v1")
	url := startProgressHertzServer(t, tracker, 19008)
	conn := dialProgressWebSocket(t, url, "/admin/progress/ws")
	defer conn.Close(websocket.StatusNormalClosure, "")
	if frame := readProgressWebSocketFrame(t, conn); frame.AppID != "app-hertz" || frame.JobID != "job-hertz" {
		t.Fatalf("unexpected hertz websocket frame: %+v", frame)
	}
}
