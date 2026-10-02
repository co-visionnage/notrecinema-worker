package mailing

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"notrecinema/worker/internal/postgres"
)

// PgStore -- Store поверх Postgres. Использует SECURITY DEFINER функции из
// миграции 0031: у воркера нет пользовательского контекста, а таблицы
// profiles и email_tokens ему напрямую недоступны (RLS).
type PgStore struct {
	db *postgres.Pool
}

func NewPgStore(db *postgres.Pool) *PgStore {
	return &PgStore{db: db}
}

func (p *PgStore) CreateEmailToken(ctx context.Context, userID, kind, tokenHash string, expiresAt time.Time) error {
	_, err := p.db.Exec(ctx, `SELECT public.create_email_token($1, $2, $3, $4)`, userID, kind, tokenHash, expiresAt)
	return err
}

func (p *PgStore) UserInfo(ctx context.Context, userID string) (UserInfo, error) {
	var (
		info        UserInfo
		displayName *string
	)
	err := p.db.QueryRow(ctx, `
		SELECT email, display_name, email_verified
		FROM public.get_user_mail_info_system($1)
	`, userID).Scan(&info.Email, &displayName, &info.Verified)
	if errors.Is(err, pgx.ErrNoRows) {
		return UserInfo{}, ErrUserNotFound
	}
	if err != nil {
		return UserInfo{}, err
	}
	if displayName != nil {
		info.DisplayName = *displayName
	}
	return info, nil
}

func (p *PgStore) FamilyRecipients(ctx context.Context, familyID string, excludeUserID *string) ([]Recipient, error) {
	rows, err := p.db.Query(ctx, `
		SELECT email, display_name
		FROM public.get_family_member_mail_info_system($1, $2)
	`, familyID, excludeUserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var recipients []Recipient
	for rows.Next() {
		var (
			r           Recipient
			displayName *string
		)
		if err := rows.Scan(&r.Email, &displayName); err != nil {
			return nil, err
		}
		if displayName != nil {
			r.DisplayName = *displayName
		}
		recipients = append(recipients, r)
	}
	return recipients, rows.Err()
}
