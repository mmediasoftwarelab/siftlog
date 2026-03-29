package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var rootCmd = &cobra.Command{
	Use:   "siftlog [file...]",
	Short: "Correlate logs across services and surface the signal",
	Long: `SiftLog ingests logs from multiple sources, correlates events across
service boundaries by timestamp and trace context, and surfaces the
specific sequence of events that preceded and constituted a failure.`,
	Args: cobra.ArbitraryArgs,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().String("config", "", "path to config file (default: ./siftlog.yaml)")
	rootCmd.PersistentFlags().String("output", "human", "output format: human | json | compact")
	rootCmd.PersistentFlags().String("window", "", "correlation window override (e.g. 1s, 250ms)")
	rootCmd.PersistentFlags().String("since", "", "start time (e.g. 15m, 1h, 2024-01-01T00:00:00Z)")
	rootCmd.PersistentFlags().String("until", "", "end time")
	rootCmd.PersistentFlags().String("services", "", "comma-separated services to include (default: all)")
	rootCmd.PersistentFlags().Bool("quiet", false, "suppress noise, show signal only")
	rootCmd.PersistentFlags().Bool("verbose", false, "include all events and structured fields")
	rootCmd.PersistentFlags().String("save", "", "save session to file for replay")
	rootCmd.PersistentFlags().Bool("live", false, "tail sources and stream events in real time")
	rootCmd.PersistentFlags().Int64("flush-ms", 0, "live mode: max ms before oldest buffered event is emitted (default 500)")

	viper.BindPFlag("output.format", rootCmd.PersistentFlags().Lookup("output"))
}
