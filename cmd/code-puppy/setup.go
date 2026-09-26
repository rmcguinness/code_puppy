package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/retail-cortex/code_puppy/internal/app"
	"github.com/retail-cortex/code_puppy/internal/config"
	"github.com/retail-cortex/code_puppy/internal/observability"
)

// globalFlags are shared by the root command and subcommands.
type globalFlags struct {
	config, dir, model, agent, agency string
	trustWorkspace                    bool
}

// loadConfig applies --dir (changing the working directory), loads trusted
// configuration and applies flag overrides.
func loadConfig(f *globalFlags) (*config.Config, error) {
	if f.dir != "" {
		if f.config != "" {
			abs, err := filepath.Abs(config.ExpandHome(f.config))
			if err != nil {
				return nil, withCode(exitUsage, fmt.Errorf("invalid --config: %w", err))
			}
			f.config = abs
		}
		if err := os.Chdir(config.ExpandHome(f.dir)); err != nil {
			return nil, withCode(exitUsage, fmt.Errorf("cannot use --dir: %w", err))
		}
	}
	cfg, err := config.Load(f.config)
	if err != nil {
		return nil, fmt.Errorf("failed to load configuration: %w", err)
	}
	if f.model != "" {
		cfg.CodePuppy.DefaultModel = f.model
	}
	if f.agent != "" {
		cfg.CodePuppy.DefaultAgent = f.agent
	}
	if f.agency != "" {
		cfg.CodePuppy.AgencyLevel = strings.ToLower(f.agency)
	}
	if f.trustWorkspace {
		cfg.CodePuppy.TrustWorkspace = true
	}
	if f.dir != "" {
		cfg.Tools.WorkspaceDir = "."
	}
	return cfg, nil
}

// startObservability opens the diagnostic log and, when enabled, OpenTelemetry
// export, and makes the log the slog default. The returned func flushes both.
// Failures only disable the affected part: diagnostics must never stop a session.
func startObservability(ctx context.Context, cfg *config.Config, warn func(string)) func() {
	r := app.SecretRedactor(cfg)
	tel, err := observability.StartTelemetry(ctx, cfg.Telemetry, version, r)
	if err != nil {
		warn("telemetry disabled: " + err.Error())
	}
	logger, logFile, err := observability.OpenLog(cfg.Log, r, tel.LogHandler())
	if err != nil {
		warn("diagnostic log disabled: " + err.Error())
		logger, logFile, _ = observability.OpenLog(config.LogConfig{Level: "off"}, r, tel.LogHandler())
	}
	slog.SetDefault(logger)
	slog.Info("start", "version", version, "provider", cfg.LLM.Provider, "model", cfg.ModelName(), "telemetry", tel != nil)
	return func() {
		if err := tel.Shutdown(context.WithoutCancel(ctx)); err != nil {
			slog.Debug("telemetry shutdown", "error", err)
		}
		logFile.Close()
	}
}
