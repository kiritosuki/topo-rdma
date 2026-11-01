package logging

import (
	"log/slog"

	"github.com/go-logr/logr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

func NewOptions() zap.Options {
	return zap.Options{
		Development: false,
	}
}

// Configure 让标准 slog.Logger 和 k8s logr.Logger 共用同一个日志结构化后端
func Configure(options *zap.Options) *slog.Logger {
	runtimeLogger := zap.New(zap.UseFlagOptions(options))
	ctrl.SetLogger(runtimeLogger)
	// 创建 slog.Logger 时来源是通过 runtimeLogger(logr.Logger) 转换
	logger := slog.New(logr.ToSlogHandler(runtimeLogger))
	slog.SetDefault(logger)
	return logger
}
