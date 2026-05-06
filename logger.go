package main

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

func (l *zapLogger) Trace(v ...interface{})  { l.sugar().Debug(v...) }
func (l *zapLogger) Debug(v ...interface{})  { l.sugar().Debug(v...) }
func (l *zapLogger) Info(v ...interface{})   { l.sugar().Info(v...) }
func (l *zapLogger) Notice(v ...interface{}) { l.sugar().Info(v...) }
func (l *zapLogger) Warn(v ...interface{})    { l.sugar().Warn(v...) }
func (l *zapLogger) Error(v ...interface{})   { l.sugar().Error(v...) }
func (l *zapLogger) Fatal(v ...interface{})   { l.sugar().Fatal(v...) }

func (l *zapLogger) Tracef(format string, v ...interface{})  { l.sugar().Debugf(format, v...) }
func (l *zapLogger) Debugf(format string, v ...interface{})  { l.sugar().Debugf(format, v...) }
func (l *zapLogger) Infof(format string, v ...interface{})   { l.sugar().Infof(format, v...) }
func (l *zapLogger) Noticef(format string, v ...interface{}) { l.sugar().Infof(format, v...) }
func (l *zapLogger) Warnf(format string, v ...interface{})   { l.sugar().Warnf(format, v...) }
func (l *zapLogger) Errorf(format string, v ...interface{})  { l.sugar().Errorf(format, v...) }
func (l *zapLogger) Fatalf(format string, v ...interface{})  { l.sugar().Fatalf(format, v...) }

func (l *zapLogger) CtxTrace(_ context.Context, v ...interface{})  { l.Trace(v...) }
func (l *zapLogger) CtxDebug(_ context.Context, v ...interface{})  { l.Debug(v...) }
func (l *zapLogger) CtxInfo(_ context.Context, v ...interface{})   { l.Info(v...) }
func (l *zapLogger) CtxNotice(_ context.Context, v ...interface{}) { l.Notice(v...) }
func (l *zapLogger) CtxWarn(_ context.Context, v ...interface{})   { l.Warn(v...) }
func (l *zapLogger) CtxError(_ context.Context, v ...interface{})  { l.Error(v...) }
func (l *zapLogger) CtxFatal(_ context.Context, v ...interface{})  { l.Fatal(v...) }

func (l *zapLogger) CtxTracef(_ context.Context, format string, v ...interface{})  { l.Tracef(format, v...) }
func (l *zapLogger) CtxDebugf(_ context.Context, format string, v ...interface{})  { l.Debugf(format, v...) }
func (l *zapLogger) CtxInfof(_ context.Context, format string, v ...interface{})   { l.Infof(format, v...) }
func (l *zapLogger) CtxNoticef(_ context.Context, format string, v ...interface{}) { l.Noticef(format, v...) }
func (l *zapLogger) CtxWarnf(_ context.Context, format string, v ...interface{})   { l.Warnf(format, v...) }
func (l *zapLogger) CtxErrorf(_ context.Context, format string, v ...interface{})  { l.Errorf(format, v...) }
func (l *zapLogger) CtxFatalf(_ context.Context, format string, v ...interface{})  { l.Fatalf(format, v...) }

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
