package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mmediasoftwarelab/siftlog/internal/adapter/file"
	"github.com/mmediasoftwarelab/siftlog/internal/config"
	"github.com/mmediasoftwarelab/siftlog/internal/correlator"
	"github.com/mmediasoftwarelab/siftlog/internal/output"
	"github.com/spf13/cobra"
)

var runCmd = &cobra.Command{
	Use:   "run [file...]",
	Short: "Start ingestion and correlation",
	Long: `Ingest logs from configured sources (or file arguments), correlate
events across service boundaries, and output the signal.

File arguments override sources defined in the config file:
  siftlog run app.log worker.log
  siftlog run -            # read from stdin`,
	RunE: runCommand,
}

func init() {
	runCmd.Flags().StringSlice("file", nil, "log file paths to ingest (overrides config sources)")
	rootCmd.AddCommand(runCmd)
	rootCmd.RunE = runCommand // make 'siftlog' without subcommand also run
}

func runCommand(cmd *cobra.Command, args []string) error {
	cfgFile, _ := cmd.Flags().GetString("config")
	if cfgFile == "" {
		cfgFile, _ = rootCmd.PersistentFlags().GetString("config")
	}

	cfg, err := config.Load(cfgFile)
	if err != nil {
		return err
	}

	since, err := parseSince(cmd)
	if err != nil {
		return err
	}
	until, err := parseUntil(cmd)
	if err != nil {
		return err
	}

	quiet, _ := cmd.Flags().GetBool("quiet")
	if !cmd.Flags().Changed("quiet") {
		quiet, _ = rootCmd.PersistentFlags().GetBool("quiet")
	}
	verbose, _ := cmd.Flags().GetBool("verbose")
	if !cmd.Flags().Changed("verbose") {
		verbose, _ = rootCmd.PersistentFlags().GetBool("verbose")
	}

	// Build source list: file args > --file flag > config sources.
	sources := buildSources(cmd, args, cfg)
	if len(sources) == 0 {
		return fmt.Errorf("no sources configured — provide file arguments, --file flag, or a siftlog.yaml config")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	corr := correlator.New(cfg)

	for _, src := range sources {
		adapter := file.New(src)
		ch, err := adapter.Fetch(ctx, since, until)
		if err != nil {
			return fmt.Errorf("source %q: %w", src.Name, err)
		}
		corr.AddSource(src.Name, ch)
	}

	outputFmt, _ := rootCmd.PersistentFlags().GetString("output")

	type writer interface {
		Header(sourceCount int)
		Write(r correlator.Result)
		Footer()
	}

	var w writer
	switch outputFmt {
	case "json":
		w = output.NewJSON(os.Stdout)
	default:
		w = output.NewHuman(os.Stdout, cfg, quiet, verbose, Version)
	}

	w.Header(len(sources))
	results := corr.Run(ctx)
	for result := range results {
		w.Write(result)
	}
	w.Footer()
	return nil
}

func buildSources(cmd *cobra.Command, args []string, cfg *config.Config) []config.SourceConfig {
	var paths []string

	// Positional args take priority.
	paths = append(paths, args...)

	// Then --file flag.
	if filePaths, err := cmd.Flags().GetStringSlice("file"); err == nil {
		paths = append(paths, filePaths...)
	}

	if len(paths) > 0 {
		sources := make([]config.SourceConfig, 0, len(paths))
		for i, p := range paths {
			name := p
			if p == "-" {
				name = "stdin"
			}
			sources = append(sources, config.SourceConfig{
				Name: fmt.Sprintf("source-%d(%s)", i+1, name),
				Type: "file",
				Path: p,
			})
		}
		return sources
	}

	// Fall back to config file sources (file type only for now).
	var sources []config.SourceConfig
	for _, s := range cfg.Sources {
		if s.Type == "file" {
			sources = append(sources, s)
		}
	}
	return sources
}

func parseSince(cmd *cobra.Command) (time.Time, error) {
	val, _ := rootCmd.PersistentFlags().GetString("since")
	return parseTimeArg(val)
}

func parseUntil(cmd *cobra.Command) (time.Time, error) {
	val, _ := rootCmd.PersistentFlags().GetString("until")
	return parseTimeArg(val)
}

func parseTimeArg(val string) (time.Time, error) {
	if val == "" {
		return time.Time{}, nil
	}

	// Try relative duration: 15m, 1h, 30s.
	if strings.HasSuffix(val, "m") || strings.HasSuffix(val, "h") || strings.HasSuffix(val, "s") {
		d, err := time.ParseDuration(val)
		if err == nil {
			return time.Now().Add(-d), nil
		}
	}

	// Try RFC3339.
	if t, err := time.Parse(time.RFC3339, val); err == nil {
		return t, nil
	}

	return time.Time{}, fmt.Errorf("unrecognized time format %q (use e.g. 15m, 1h, or 2024-01-01T00:00:00Z)", val)
}
