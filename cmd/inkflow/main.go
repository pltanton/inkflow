package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"inkflow/internal/ai"
	"inkflow/internal/ai/gemini"
	"inkflow/internal/config"
	"inkflow/internal/importer"
	"inkflow/internal/log"
	"inkflow/internal/retry"
	"inkflow/internal/state"
	"inkflow/internal/webdavserver"
)

// version and commit are injected at build time via -ldflags.
var (
	version = "dev"
	commit  = "unknown"
)

type runtime struct {
	logger    *slog.Logger
	cfg       *config.Config
	store     *state.Store
	imp       *importer.Importer
	scheduler *retry.Scheduler
}

var rt runtime

func main() {
	logger := log.New()
	slog.SetDefault(logger)
	root := newRootCmd(logger)
	if err := root.ExecuteContext(context.Background()); err != nil {
		logger.Error("inkflow failed", "err", err)
		os.Exit(1)
	}
}

func newRootCmd(logger *slog.Logger) *cobra.Command {
	var configPath string
	cmd := &cobra.Command{
		Use:           "inkflow",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			loaded, err := loadRuntime(logger, configPath)
			if err != nil {
				return err
			}
			rt = loaded
			return nil
		},
		PersistentPostRun: func(cmd *cobra.Command, args []string) {
			if rt.store != nil {
				_ = rt.store.Close()
			}
		},
	}
	cmd.PersistentFlags().StringVarP(&configPath, "config", "c", "inkflow.toml", "config file")
	cmd.AddCommand(newVersionCmd())
	cmd.AddCommand(newServeCmd())
	return cmd
}

func loadRuntime(logger *slog.Logger, configPath string) (runtime, error) {
	cfg, cfgDir, err := config.Load(configPath)
	if err != nil {
		return runtime{}, err
	}

	statePath := cfg.StateFile
	if statePath == "" {
		statePath = defaultStatePath()
	} else if !filepath.IsAbs(statePath) {
		statePath = filepath.Join(cfgDir, statePath)
	}
	if cfg.TemplateDir != "" && !filepath.IsAbs(cfg.TemplateDir) {
		cfg.TemplateDir = filepath.Join(cfgDir, cfg.TemplateDir)
	}
	if cfg.Upstream.URL != "" {
		pass, err := resolveUpstreamPassword(cfg.Upstream)
		if err != nil {
			return runtime{}, err
		}
		cfg.Upstream.Password = pass
	}
	var aiProvider ai.Provider
	if anyRouteWantsAI(cfg.Routes) {
		key, err := resolveAPIKey(cfg.Gemini)
		if err != nil {
			return runtime{}, err
		}
		timeout, err := time.ParseDuration(cfg.Gemini.Timeout)
		if err != nil {
			return runtime{}, fmt.Errorf("parse gemini timeout: %w", err)
		}
		aiProvider = gemini.New(gemini.ClientConfig{
			APIKey:        key,
			Model:         cfg.Gemini.Model,
			Timeout:       timeout,
			OCRPrompt:     cfg.Gemini.OCRPrompt,
			SummaryPrompt: cfg.Gemini.SummaryPrompt,
		})
	}
	store, err := state.Open(statePath)
	if err != nil {
		return runtime{}, err
	}
	imp := importer.New(cfg, store, aiProvider)

	var sched *retry.Scheduler
	if cfg.Gemini.Retry.Enabled {
		sched = retry.NewScheduler(store, imp, cfg.Gemini.Retry)
	}

	return runtime{logger: logger, cfg: cfg, store: store, imp: imp, scheduler: sched}, nil
}

func defaultStatePath() string {
	if base := os.Getenv("XDG_STATE_HOME"); base != "" {
		return filepath.Join(base, "inkflow", "state.db")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".", ".local", "state", "inkflow", "state.db")
	}
	return filepath.Join(home, ".local", "state", "inkflow", "state.db")
}

func anyRouteWantsAI(routes []config.Route) bool {
	for _, r := range routes {
		if r.AI {
			return true
		}
	}
	return false
}

func resolveAPIKey(cfg config.GeminiConfig) (string, error) {
	if key := strings.TrimSpace(os.Getenv("GEMINI_API_KEY")); key != "" {
		return key, nil
	}
	if cfg.APIKeyFile != "" {
		data, err := os.ReadFile(cfg.APIKeyFile)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", cfg.APIKeyFile, err)
		}
		if key := strings.TrimSpace(string(data)); key != "" {
			return key, nil
		}
	}
	return "", fmt.Errorf("gemini: no API key — set $GEMINI_API_KEY or [gemini].api_key_file")
}

func resolveUpstreamPassword(cfg config.UpstreamConfig) (string, error) {
	data, err := os.ReadFile(cfg.PasswordFile)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", cfg.PasswordFile, err)
	}
	pass := strings.TrimSpace(string(data))
	if pass == "" {
		return "", fmt.Errorf("upstream: %s is empty", cfg.PasswordFile)
	}
	return pass, nil
}

func newServeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Serve BOOX uploads over WebDAV",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if rt.scheduler != nil {
				rt.scheduler.Start(cmd.Context())
			}
			err := webdavserver.Serve(cmd.Context(), rt.cfg, rt.imp, rt.logger)
			if rt.scheduler != nil {
				rt.scheduler.Stop()
			}
			return err
		},
	}
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show build version",
		Args:  cobra.NoArgs,
		// Override PersistentPreRunE so config is not loaded for this command.
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error { return nil },
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("inkflow %s (%s)\n", version, commit)
		},
	}
}
