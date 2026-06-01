package admin

import (
	"context"
	"io"
	"os"

	"github.com/cloudwego/hertz/pkg/common/hlog"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type zapLogger struct {
	logger  *zap.Logger
	atomic  zap.AtomicLevel
	encoder zapcore.EncoderConfig
	output  io.Writer
}

func NewZapLogger() (*zapLogger, error) {
	cfg := zap.NewProductionConfig()
	cfg.Encoding = "json"
	cfg.Level = zap.NewAtomicLevelAt(zap.InfoLevel)
	logger, err := cfg.Build()
	if err != nil {
		return nil, err
	}
	return &zapLogger{
		logger:  logger,
		atomic:  cfg.Level,
		encoder: cfg.EncoderConfig,
		output:  os.Stderr,
	}, nil
}

func (l *zapLogger) rebuild() error {
	if l.output == nil {
		l.output = os.Stderr
	}
	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(l.encoder),
		zapcore.AddSync(l.output),
		l.atomic,
	)
	if l.logger != nil {
		_ = l.logger.Sync()
	}
	l.logger = zap.New(core, zap.AddCaller(), zap.AddCallerSkip(1))
	return nil
}

func (l *zapLogger) sugar() *zap.SugaredLogger {
	if l == nil || l.logger == nil {
		logger, _ := NewZapLogger()
		return logger.logger.Sugar()
	}
	return l.logger.Sugar()
}

func (l *zapLogger) Trace(v ...any)  { l.sugar().Debug(v...) }
func (l *zapLogger) Debug(v ...any)  { l.sugar().Debug(v...) }
func (l *zapLogger) Info(v ...any)   { l.sugar().Info(v...) }
func (l *zapLogger) Notice(v ...any) { l.sugar().Info(v...) }
func (l *zapLogger) Warn(v ...any)   { l.sugar().Warn(v...) }
func (l *zapLogger) Error(v ...any)  { l.sugar().Error(v...) }
func (l *zapLogger) Fatal(v ...any)  { l.sugar().Fatal(v...) }

func (l *zapLogger) Tracef(format string, v ...any)  { l.sugar().Debugf(format, v...) }
func (l *zapLogger) Debugf(format string, v ...any)  { l.sugar().Debugf(format, v...) }
func (l *zapLogger) Infof(format string, v ...any)   { l.sugar().Infof(format, v...) }
func (l *zapLogger) Noticef(format string, v ...any) { l.sugar().Infof(format, v...) }
func (l *zapLogger) Warnf(format string, v ...any)   { l.sugar().Warnf(format, v...) }
func (l *zapLogger) Errorf(format string, v ...any)  { l.sugar().Errorf(format, v...) }
func (l *zapLogger) Fatalf(format string, v ...any)  { l.sugar().Fatalf(format, v...) }

func (l *zapLogger) CtxTrace(_ context.Context, v ...any)  { l.Trace(v...) }
func (l *zapLogger) CtxDebug(_ context.Context, v ...any)  { l.Debug(v...) }
func (l *zapLogger) CtxInfo(_ context.Context, v ...any)   { l.Info(v...) }
func (l *zapLogger) CtxNotice(_ context.Context, v ...any) { l.Notice(v...) }
func (l *zapLogger) CtxWarn(_ context.Context, v ...any)   { l.Warn(v...) }
func (l *zapLogger) CtxError(_ context.Context, v ...any)  { l.Error(v...) }
func (l *zapLogger) CtxFatal(_ context.Context, v ...any)  { l.Fatal(v...) }

func (l *zapLogger) CtxTracef(_ context.Context, format string, v ...any)  { l.Tracef(format, v...) }
func (l *zapLogger) CtxDebugf(_ context.Context, format string, v ...any)  { l.Debugf(format, v...) }
func (l *zapLogger) CtxInfof(_ context.Context, format string, v ...any)   { l.Infof(format, v...) }
func (l *zapLogger) CtxNoticef(_ context.Context, format string, v ...any) { l.Noticef(format, v...) }
func (l *zapLogger) CtxWarnf(_ context.Context, format string, v ...any)   { l.Warnf(format, v...) }
func (l *zapLogger) CtxErrorf(_ context.Context, format string, v ...any)  { l.Errorf(format, v...) }
func (l *zapLogger) CtxFatalf(_ context.Context, format string, v ...any)  { l.Fatalf(format, v...) }

func (l *zapLogger) SetLevel(level hlog.Level) {
	switch level {
	case hlog.LevelTrace, hlog.LevelDebug:
		l.atomic.SetLevel(zap.DebugLevel)
	case hlog.LevelInfo, hlog.LevelNotice:
		l.atomic.SetLevel(zap.InfoLevel)
	case hlog.LevelWarn:
		l.atomic.SetLevel(zap.WarnLevel)
	case hlog.LevelError:
		l.atomic.SetLevel(zap.ErrorLevel)
	default:
		l.atomic.SetLevel(zap.InfoLevel)
	}
}

func (l *zapLogger) SetOutput(w io.Writer) {
	l.output = w
	_ = l.rebuild()
}

func (l *zapLogger) Sync() {
	if l.logger != nil {
		_ = l.logger.Sync()
	}
}

var _ hlog.FullLogger = (*zapLogger)(nil)
