package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"inkflow/internal/config"
	"inkflow/internal/importer"
	"inkflow/internal/log"
	"inkflow/internal/state"
	"inkflow/internal/webdavserver"
)

type runtime struct {
	logger *slog.Logger
	cfg    *config.Config
	store  *state.Store
	imp    *importer.Importer
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
	store, err := state.Open(statePath)
	if err != nil {
		return runtime{}, err
	}
	imp := importer.New(cfg, store)
	return runtime{logger: logger, cfg: cfg, store: store, imp: imp}, nil
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

func newServeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Serve BOOX uploads over WebDAV",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return webdavserver.Serve(cmd.Context(), rt.cfg, rt.imp, rt.logger)
		},
	}
}
