package cli

import (
	"fmt"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/varve-sh/varve/internal/util"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Read and write global varve settings",
	}
	cmd.AddCommand(newConfigSetCmd(), newConfigGetCmd(), newConfigUnsetCmd())
	return cmd
}

// Supported keys and where they map in EmbedConfig.
var configKeys = map[string]string{
	"embed.key":      "Embedding API key (VARVE_EMBED_KEY / OPENAI_API_KEY)",
	"embed.url":      "Embedding API base URL (VARVE_EMBED_URL)",
	"embed.model":    "Embedding model name (VARVE_EMBED_MODEL)",
	"embed.provider": `Embedding provider override: "auto" (default) or "disabled"`,
}

func newConfigSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set a config value",
		Long:  "Set a config value.\n\nKeys: embed.key, embed.url, embed.model, embed.provider",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			key, value := strings.ToLower(args[0]), args[1]
			if _, ok := configKeys[key]; !ok {
				return fmt.Errorf("unknown key %q — valid keys: embed.key, embed.url, embed.model, embed.provider", key)
			}

			cfg := util.GetProjectConfig()
			applyEmbedKey(cfg, key, value)
			if err := util.SaveProjectConfig(cfg); err != nil {
				return fmt.Errorf("saving config: %w", err)
			}

			fmt.Printf("Set %s\n", key)
			return nil
		},
	}
}

func newConfigUnsetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unset <key>",
		Short: "Remove a config value",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := strings.ToLower(args[0])
			if _, ok := configKeys[key]; !ok {
				return fmt.Errorf("unknown key %q — valid keys: embed.key, embed.url, embed.model, embed.provider", key)
			}

			cfg := util.GetProjectConfig()
			applyEmbedKey(cfg, key, "")
			if err := util.SaveProjectConfig(cfg); err != nil {
				return fmt.Errorf("saving config: %w", err)
			}

			fmt.Printf("Unset %s\n", key)
			return nil
		},
	}
}

func newConfigGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get",
		Short: "Show current config values",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := util.GetProjectConfig()
			bold := color.New(color.Bold)
			dim := color.New(color.Faint)

			bold.Println("Embed settings  (env vars override these)")
			printConfigRow(dim, "embed.key", maskKey(cfg.Embed.Key))
			printConfigRow(dim, "embed.url", cfg.Embed.URL)
			printConfigRow(dim, "embed.model", cfg.Embed.Model)
			printConfigRow(dim, "embed.provider", cfg.Embed.Provider)
			return nil
		},
	}
}

func applyEmbedKey(cfg *util.ProjectConfig, key, value string) {
	switch key {
	case "embed.key":
		cfg.Embed.Key = value
	case "embed.url":
		cfg.Embed.URL = value
	case "embed.model":
		cfg.Embed.Model = value
	case "embed.provider":
		cfg.Embed.Provider = value
	}
}

func printConfigRow(dim *color.Color, key, value string) {
	if value == "" {
		dim.Printf("  %-14s (not set)\n", key)
	} else {
		fmt.Printf("  %-14s %s\n", key, value)
	}
}

func maskKey(s string) string {
	if len(s) <= 8 {
		return strings.Repeat("*", len(s))
	}
	return s[:4] + strings.Repeat("*", len(s)-8) + s[len(s)-4:]
}
