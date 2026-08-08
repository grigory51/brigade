//go:build !darwin || !cgo

package desktopenv

import (
	"context"
	"errors"
)

func (m *Manager) platformMount(context.Context, string, Mount) (mountHandle, error) {
	return nil, errors.New("mount поддерживается только Brigade.app на macOS")
}
