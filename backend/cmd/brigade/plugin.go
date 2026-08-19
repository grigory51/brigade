package main

import (
	"context"
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/grigory51/brigade/backend/internal/config"
	pluginruntime "github.com/grigory51/brigade/backend/internal/plugin"
	"github.com/grigory51/brigade/backend/internal/secret"
	"github.com/grigory51/brigade/backend/internal/store"
)

func newPluginCommand(configPath *string) *cobra.Command {
	plugin := &cobra.Command{Use: "plugin", Short: "управление MCPB-плагинами"}
	plugin.AddCommand(
		&cobra.Command{Use: "install <file-or-url>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			return withPluginManager(cmd.Context(), *configPath, func(manager *pluginruntime.Manager, _ *store.Store) error {
				installed, err := manager.Install(cmd.Context(), args[0])
				if err == nil {
					fmt.Fprintf(cmd.OutOrStdout(), "%s %s installed\n", installed.ID, installed.Version)
				}
				return err
			})
		}},
		&cobra.Command{Use: "update <id>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			return withPluginManager(cmd.Context(), *configPath, func(manager *pluginruntime.Manager, _ *store.Store) error {
				installed, err := manager.Update(cmd.Context(), args[0])
				if err == nil {
					fmt.Fprintf(cmd.OutOrStdout(), "%s %s installed\n", installed.ID, installed.Version)
				}
				return err
			})
		}},
		&cobra.Command{Use: "remove <id>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			return withPluginManager(cmd.Context(), *configPath, func(manager *pluginruntime.Manager, _ *store.Store) error {
				return manager.Remove(cmd.Context(), args[0])
			})
		}},
		&cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			return withPluginManager(cmd.Context(), *configPath, func(_ *pluginruntime.Manager, st *store.Store) error {
				plugins, err := st.ListPlugins(cmd.Context())
				if err != nil {
					return err
				}
				w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
				fmt.Fprintln(w, "ID\tVERSION\tNAME")
				for _, item := range plugins {
					fmt.Fprintf(w, "%s\t%s\t%s\n", item.ID, item.Version, item.Name)
				}
				return w.Flush()
			})
		}},
		&cobra.Command{Use: "validate <file>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			manager := pluginruntime.New("", nil)
			manifest, err := manager.ValidateBundle(args[0])
			if err == nil {
				fmt.Fprintf(cmd.OutOrStdout(), "%s %s is a valid Brigade MCPB experience\n", manifest.Name, manifest.Version)
			}
			return err
		}},
	)
	return plugin
}

func withPluginManager(ctx context.Context, configPath string, fn func(*pluginruntime.Manager, *store.Store) error) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	st, err := store.Open(cfg.SQLitePath, secret.NewCipher(cfg.JWT.Secret))
	if err != nil {
		return err
	}
	defer st.Close()
	return fn(pluginruntime.New(cfg.PluginsDir, st), st)
}
