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
	"inkflow/internal/ai/openai"
	"inkflow/internal/config"
	"inkflow/internal/importer"
	"inkflow/internal/log"
	"inkflow/internal/plan"
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
	cmd.AddCommand(newCheckCmd(&configPath))
	return cmd
}

func newCheckCmd(configPath *string) *cobra.Command {
	var sampleFilename string
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Check configuration and filesystem prerequisites",
		Args:  cobra.NoArgs,
		// Checking must not construct a runtime, open state, or start a server.
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error { return nil },
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, cfgDir, err := config.Load(*configPath)
			if err != nil {
				return err
			}
			if cfg.TemplateDir != "" && !filepath.IsAbs(cfg.TemplateDir) {
				cfg.TemplateDir = filepath.Join(cfgDir, cfg.TemplateDir)
			}
			out := cmd.OutOrStdout()
			fmt.Fprintln(out, "Resolved routes:")
			for i, route := range cfg.Routes {
				fmt.Fprintf(out, "  %d: %s (pdf_dir=%q note_dir=%q ai=%t)\n", i+1, config.NormalizeRoutePrefix(route.From), route.PDFDir, route.NoteDir, route.AI)
			}

			var findings []string
			if err := checkDirectory("vault_dir", cfg.VaultDir); err != nil {
				findings = append(findings, err.Error())
			}
			if cfg.TemplateDir != "" {
				if err := checkDirectory("template_dir", cfg.TemplateDir); err != nil {
					findings = append(findings, err.Error())
				}
			}
			if anyRouteWantsAI(cfg.Routes) {
				if cfg.AI.Provider == "openai" {
					_, err = resolveOpenAIAPIKey(cfg.OpenAI)
				} else {
					_, err = resolveGeminiAPIKey(cfg.Gemini)
				}
				if err != nil {
					findings = append(findings, err.Error())
				}
			}
			if sampleFilename != "" {
				result, err := plan.Build(cfg.Routes, cfg, sampleFilename, time.Now().UTC())
				if err != nil {
					findings = append(findings, fmt.Sprintf("sample filename: %v", err))
				} else {
					match, _ := plan.Select(cfg.Routes, sampleFilename)
					fmt.Fprintf(out, "Sample plan:\n  route: %s\n  PDFRel: %s\n  NoteRel: %s\n  tags: %v\n  date: %s\n  title: %s\n", config.NormalizeRoutePrefix(match.Route.From), result.PDFRel, result.NoteRel, result.Tags, result.Date.Format("2006-01-02"), result.Title)
				}
			}
			if len(findings) > 0 {
				for _, finding := range findings {
					fmt.Fprintf(out, "FAIL: %s\n", finding)
				}
				return fmt.Errorf("preflight failed")
			}
			fmt.Fprintln(out, "Preflight successful.")
			return nil
		},
	}
	cmd.Flags().StringVar(&sampleFilename, "sample-filename", "", "sample filename to plan")
	return cmd
}

func checkDirectory(name, dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("%s %q: %w", name, dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s %q is not a directory", name, dir)
	}
	if info.Mode().Perm()&0222 == 0 {
		return fmt.Errorf("%s %q is not writable", name, dir)
	}
	return nil
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
	var aiProvider ai.Provider
	if anyRouteWantsAI(cfg.Routes) {
		switch cfg.AI.Provider {
		case "", "gemini":
			key, err := resolveGeminiAPIKey(cfg.Gemini)
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
		case "openai":
			key, err := resolveOpenAIAPIKey(cfg.OpenAI)
			if err != nil {
				return runtime{}, err
			}
			timeout, err := time.ParseDuration(cfg.OpenAI.Timeout)
			if err != nil {
				return runtime{}, fmt.Errorf("parse openai timeout: %w", err)
			}
			aiProvider = openai.New(openai.ClientConfig{
				APIKey:        key,
				Model:         cfg.OpenAI.Model,
				Timeout:       timeout,
				OCRPrompt:     cfg.OpenAI.OCRPrompt,
				SummaryPrompt: cfg.OpenAI.SummaryPrompt,
			})
		default:
			return runtime{}, fmt.Errorf("unknown AI provider: %q", cfg.AI.Provider)
		}
	}
	store, err := state.Open(statePath)
	if err != nil {
		return runtime{}, err
	}
	locks := importer.NewLockManager()
	imp := importer.New(cfg, store, aiProvider, cfg.Gemini.MinReprocessIntervalDuration, locks)

	// The retry scheduler is also the durable queue worker. It must run even
	// when failed-import retries are disabled so pending uploads are processed.
	var sched *retry.Scheduler
	if anyRouteWantsAI(cfg.Routes) {
		sched = retry.NewScheduler(store, imp, cfg.Gemini.Retry, locks)
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

func resolveGeminiAPIKey(cfg config.GeminiConfig) (string, error) {
	return resolveAPIKey("gemini", "GEMINI_API_KEY", cfg.APIKeyFile)
}

func resolveOpenAIAPIKey(cfg config.OpenAIConfig) (string, error) {
	return resolveAPIKey("openai", "OPENAI_API_KEY", cfg.APIKeyFile)
}

func resolveAPIKey(provider, envVar, keyFile string) (string, error) {
	if key := strings.TrimSpace(os.Getenv(envVar)); key != "" {
		return key, nil
	}
	if keyFile != "" {
		data, err := os.ReadFile(keyFile)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", keyFile, err)
		}
		if key := strings.TrimSpace(string(data)); key != "" {
			return key, nil
		}
	}
	return "", fmt.Errorf("%s: no API key — set $%s or [%s].api_key_file", provider, envVar, provider)
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
