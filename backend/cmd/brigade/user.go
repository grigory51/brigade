package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/grigory51/brigade/backend/internal/config"
	"github.com/grigory51/brigade/backend/internal/secret"
	"github.com/grigory51/brigade/backend/internal/store"
)

func newUserCommand(configPath *string) *cobra.Command {
	var confirmDelete bool
	list := &cobra.Command{
		Use:   "list",
		Short: "показать пользователей и способы входа",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return listUsers(cmd.Context(), cmd, *configPath)
		},
	}
	migrate := &cobra.Command{
		Use:   "migrate <old-id> <new-id>",
		Short: "перенести все данные старого пользователя в нового",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return migrateUser(cmd.Context(), cmd, *configPath, args[0], args[1])
		},
	}
	remove := &cobra.Command{
		Use:   "delete <id>",
		Short: "удалить пользователя без сессий",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !confirmDelete {
				return errors.New("user delete: подтвердите удаление флагом --yes")
			}
			return deleteUser(cmd.Context(), cmd, *configPath, args[0])
		},
	}
	remove.Flags().BoolVar(&confirmDelete, "yes", false, "подтвердить необратимое удаление")
	user := &cobra.Command{Use: "user", Short: "управление пользователями"}
	user.AddCommand(list, migrate, remove)
	return user
}

func deleteUser(ctx context.Context, cmd *cobra.Command, configPath, userID string) error {
	if filepath.Base(userID) != userID {
		return errors.New("user delete: некорректный user id")
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	st, err := store.Open(cfg.SQLitePath, secret.NewCipher(cfg.JWT.Secret))
	if err != nil {
		return err
	}
	defer st.Close()
	user, err := st.GetUserByID(ctx, userID)
	if err != nil {
		return fmt.Errorf("user delete: пользователь %q: %w", userID, err)
	}
	if err := st.DeleteUser(ctx, userID); err != nil {
		return err
	}
	for _, root := range userDataRoots(cfg) {
		if root != "" {
			if err := os.RemoveAll(filepath.Join(root, userID)); err != nil {
				return fmt.Errorf("user delete: пользователь удалён из БД, удалить каталог %q: %w", filepath.Join(root, userID), err)
			}
		}
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Пользователь %s (%s) удалён. Перезапустите Brigade.\n", user.Username, user.ID)
	return nil
}

func listUsers(ctx context.Context, cmd *cobra.Command, configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	st, err := store.Open(cfg.SQLitePath, secret.NewCipher(cfg.JWT.Secret))
	if err != nil {
		return err
	}
	defer st.Close()
	users, err := st.ListUsers(ctx)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tUSERNAME\tAUTH")
	for _, user := range users {
		auth := make([]string, 0, 2)
		if user.Password {
			auth = append(auth, "password")
		}
		if user.Providers != "" {
			auth = append(auth, "OIDC "+user.Providers)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", user.ID, user.Username, strings.Join(auth, ", "))
	}
	return w.Flush()
}

func migrateUser(ctx context.Context, cmd *cobra.Command, configPath, oldID, newID string) error {
	if oldID == newID || filepath.Base(oldID) != oldID || filepath.Base(newID) != newID {
		return errors.New("user migrate: нужны два разных корректных user id")
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	st, err := store.Open(cfg.SQLitePath, secret.NewCipher(cfg.JWT.Secret))
	if err != nil {
		return err
	}
	defer st.Close()

	oldUser, err := st.GetUserByID(ctx, oldID)
	if err != nil {
		return fmt.Errorf("user migrate: старый пользователь %q: %w", oldID, err)
	}
	newUser, err := st.GetUserByID(ctx, newID)
	if err != nil {
		return fmt.Errorf("user migrate: новый пользователь %q: %w", newID, err)
	}

	moved, err := moveUserDirectories(userDataRoots(cfg), oldID, newID)
	if err != nil {
		return err
	}
	if err := st.MigrateUser(ctx, oldID, newID); err != nil {
		rollbackUserDirectories(moved)
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Пользователь %s (%s) перенесён в %s (%s). Перезапустите Brigade.\n", oldUser.Username, oldID, newUser.Username, newID)
	return nil
}

type movedDirectory struct{ from, to string }

func userDataRoots(cfg *config.Config) []string {
	roots := []string{cfg.Memory.Dir, cfg.AgentHomeDir}
	if cfg.PluginsDir != "" {
		roots = append(roots, filepath.Join(cfg.PluginsDir, "users"))
	}
	return roots
}

func moveUserDirectories(roots []string, oldID, newID string) ([]movedDirectory, error) {
	var pending []movedDirectory
	seen := map[string]bool{}
	for _, root := range roots {
		if root == "" || seen[root] {
			continue
		}
		seen[root] = true
		from, to := filepath.Join(root, oldID), filepath.Join(root, newID)
		info, err := os.Stat(from)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("user migrate: проверить %q: %w", from, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("user migrate: %q не каталог", from)
		}
		if _, err := os.Stat(to); err == nil {
			return nil, fmt.Errorf("user migrate: целевой каталог %q уже существует", to)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("user migrate: проверить %q: %w", to, err)
		}
		pending = append(pending, movedDirectory{from: from, to: to})
	}

	var moved []movedDirectory
	for _, item := range pending {
		if err := os.Rename(item.from, item.to); err != nil {
			rollbackUserDirectories(moved)
			return nil, fmt.Errorf("user migrate: перенести %q: %w", item.from, err)
		}
		moved = append(moved, item)
	}
	return moved, nil
}

func rollbackUserDirectories(moved []movedDirectory) {
	for i := len(moved) - 1; i >= 0; i-- {
		_ = os.Rename(moved[i].to, moved[i].from)
	}
}
