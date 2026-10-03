package mailing

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"notrecinema/worker/internal/emails"
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

func (p *PgStore) UserEmailEnabled(ctx context.Context, userID, category string) (bool, error) {
	var enabled bool
	err := p.db.QueryRow(ctx, `SELECT public.notification_enabled($1, $2, 'email')`, userID, category).Scan(&enabled)
	return enabled, err
}

func (p *PgStore) FamilyRecipients(ctx context.Context, familyID string, excludeUserID *string, category string) ([]Recipient, error) {
	rows, err := p.db.Query(ctx, `
		SELECT user_id, email, display_name
		FROM public.get_family_member_mail_info_system($1, $2, $3)
	`, familyID, excludeUserID, category)
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
		if err := rows.Scan(&r.UserID, &r.Email, &displayName); err != nil {
			return nil, err
		}
		if displayName != nil {
			r.DisplayName = *displayName
		}
		recipients = append(recipients, r)
	}
	return recipients, rows.Err()
}

// WeeklyDigest читает сводку одной функцией БД и раскладывает строки по семьям
// (функция уже отдаёт их по семьям подряд, по разделам и времени).
func (p *PgStore) WeeklyDigest(ctx context.Context, userID string, since, until time.Time) ([]emails.DigestFamily, error) {
	rows, err := p.db.Query(ctx, `
		SELECT family_id, family_name, section, title, detail, happened_at
		FROM public.get_weekly_digest_system($1, $2, $3)
	`, userID, since, until)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var (
		families []emails.DigestFamily
		current  = -1
		lastID   string
	)
	for rows.Next() {
		var (
			familyID, familyName, section, title, detail string
			at                                           time.Time
		)
		if err := rows.Scan(&familyID, &familyName, &section, &title, &detail, &at); err != nil {
			return nil, err
		}
		if familyID != lastID {
			families = append(families, emails.DigestFamily{Name: familyName})
			current++
			lastID = familyID
		}
		item := emails.DigestItem{Title: title, Detail: detail, At: at}
		family := &families[current]
		switch section {
		case "added":
			family.Added = append(family.Added, item)
		case "watched":
			family.Watched = append(family.Watched, item)
		case "event":
			family.Events = append(family.Events, item)
		case "airing":
			family.Airing = append(family.Airing, item)
		}
	}
	return families, rows.Err()
}

func (p *PgStore) InvitationInfo(ctx context.Context, invitationID string) (InvitationInfo, error) {
	var info InvitationInfo
	err := p.db.QueryRow(ctx, `
		SELECT email, family_name, inviter_name
		FROM public.get_family_invitation_mail_info_system($1)
	`, invitationID).Scan(&info.Email, &info.FamilyName, &info.InviterName)
	if errors.Is(err, pgx.ErrNoRows) {
		return InvitationInfo{}, ErrInvitationNotFound
	}
	return info, err
}

func (p *PgStore) CreateInvitationToken(ctx context.Context, invitationID, tokenHash string, expiresAt time.Time) (bool, error) {
	var stored bool
	err := p.db.QueryRow(ctx, `
		SELECT public.create_family_invitation_token($1, $2, $3)
	`, invitationID, tokenHash, expiresAt).Scan(&stored)
	return stored, err
}
