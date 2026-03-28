package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/viper"
)

type Config struct {
	Sources     []SourceConfig     `mapstructure:"sources"`
	Correlation CorrelationConfig  `mapstructure:"correlation"`
	Signal      SignalConfig       `mapstructure:"signal"`
	Output      OutputConfig       `mapstructure:"output"`
	Session     SessionConfig      `mapstructure:"session"`
}

type SourceConfig struct {
	Name              string            `mapstructure:"name"`
	Type              string            `mapstructure:"type"`
	Path              string            `mapstructure:"path"`   // file adapter
	URL               string            `mapstructure:"url"`    // loki, es
	Region            string            `mapstructure:"region"` // cloudwatch
	LogGroups         []string          `mapstructure:"log_groups"`
	Auth              AuthConfig        `mapstructure:"auth"`
	Labels            map[string]string `mapstructure:"labels"`
	TimestampOffsetMs int64             `mapstructure:"timestamp_offset_ms"`
}

type AuthConfig struct {
	Type      string `mapstructure:"type"`
	TokenEnv  string `mapstructure:"token_env"`
	APIKey    string `mapstructure:"api_key"`
}

type CorrelationConfig struct {
	Anchor             string `mapstructure:"anchor"`
	WindowMs           int64  `mapstructure:"window_ms"`
	DriftToleranceMs   int64  `mapstructure:"drift_tolerance_ms"`
}

type SignalConfig struct {
	AnomalyThresholdMultiplier float64 `mapstructure:"anomaly_threshold_multiplier"`
	CascadeDetection           bool    `mapstructure:"cascade_detection"`
	SilenceDetection           bool    `mapstructure:"silence_detection"`
	SilenceThresholdPct        float64 `mapstructure:"silence_threshold_pct"`
	BaselineWindowMinutes      int     `mapstructure:"baseline_window_minutes"`
}

type OutputConfig struct {
	Timestamps       string `mapstructure:"timestamps"`
	SeverityColors   bool   `mapstructure:"severity_colors"`
	MaxLinesPerEvent int    `mapstructure:"max_lines_per_event"`
	ShowServiceName  bool   `mapstructure:"show_service_name"`
}

type SessionConfig struct {
	SaveDir       string `mapstructure:"save_dir"`
	RetentionDays int    `mapstructure:"retention_days"`
}

func defaults() {
	viper.SetDefault("correlation.anchor", "sender")
	viper.SetDefault("correlation.window_ms", 500)
	viper.SetDefault("correlation.drift_tolerance_ms", 200)
	viper.SetDefault("signal.anomaly_threshold_multiplier", 10.0)
	viper.SetDefault("signal.cascade_detection", true)
	viper.SetDefault("signal.silence_detection", true)
	viper.SetDefault("signal.silence_threshold_pct", 90.0)
	viper.SetDefault("signal.baseline_window_minutes", 5)
	viper.SetDefault("output.timestamps", "relative")
	viper.SetDefault("output.severity_colors", true)
	viper.SetDefault("output.max_lines_per_event", 5)
	viper.SetDefault("output.show_service_name", true)
	viper.SetDefault("session.save_dir", "~/.siftlog/sessions")
	viper.SetDefault("session.retention_days", 7)
}

func Load(cfgFile string) (*Config, error) {
	defaults()

	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
	} else {
		viper.SetConfigName("siftlog")
		viper.SetConfigType("yaml")
		viper.AddConfigPath(".")
		viper.AddConfigPath("$HOME/.siftlog")
	}

	viper.SetEnvPrefix("SIFTLOG")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	viper.AutomaticEnv()

	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("config error: %w", err)
		}
		// no config file is fine — defaults + flags cover it
	}

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("config parse error: %w", err)
	}

	return &cfg, nil
}

func (c *CorrelationConfig) Window() time.Duration {
	return time.Duration(c.WindowMs) * time.Millisecond
}

func (c *CorrelationConfig) DriftTolerance() time.Duration {
	return time.Duration(c.DriftToleranceMs) * time.Millisecond
}
