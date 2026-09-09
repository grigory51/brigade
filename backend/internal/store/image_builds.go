package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

type ImageBuild struct {
	ID     string
	Name   string
	Script string
	Status string
	Log    string
	Error  string
	Image  string
}

func (s *Store) GetImageBuild(ctx context.Context, userID string) (ImageBuild, error) {
	var b ImageBuild
	err := s.db.QueryRowContext(ctx, `SELECT id, name, script, status, log, error, image FROM image_builds WHERE user_id = ?`, userID).
		Scan(&b.ID, &b.Name, &b.Script, &b.Status, &b.Log, &b.Error, &b.Image)
	if errors.Is(err, sql.ErrNoRows) {
		return b, nil
	}
	return b, err
}

func (s *Store) StartImageBuild(ctx context.Context, userID string, b ImageBuild) error {
	res, err := s.db.ExecContext(ctx, `INSERT INTO image_builds (user_id,id,name,script,status,log,error,image)
		SELECT ?,?,?,?,?,?,?,? WHERE EXISTS (SELECT 1 FROM users WHERE id = ?)
		ON CONFLICT(user_id) DO UPDATE SET id=excluded.id,name=excluded.name,script=excluded.script,status=excluded.status,log=excluded.log,error=excluded.error,image=excluded.image`,
		userID, b.ID, b.Name, b.Script, b.Status, b.Log, b.Error, b.Image, userID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("image build: пользователь не найден")
	}
	return nil
}

func (s *Store) UpdateImageBuild(ctx context.Context, userID string, b ImageBuild) error {
	res, err := s.db.ExecContext(ctx, `UPDATE image_builds SET status=?,log=?,error=?,image=? WHERE user_id=? AND id=?`,
		b.Status, b.Log, b.Error, b.Image, userID, b.ID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("image build: сборка удалена или перенесена")
	}
	return nil
}

func (s *Store) InterruptImageBuilds(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE image_builds SET status='interrupted',error='Brigade перезапущен во время сборки. Запустите сборку повторно.' WHERE status IN ('queued','building')`)
	return err
}
