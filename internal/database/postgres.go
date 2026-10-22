package database

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func Connect(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	if databaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is empty; configure it in .env")
	}
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	cfg.MaxConns = 10
	cfg.MinConns = 1
	cfg.MaxConnLifetime = time.Hour

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create PostgreSQL pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping PostgreSQL: %w", err)
	}
	return pool, nil
}

func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	const schema = `
CREATE TABLE IF NOT EXISTS recordings (
    id UUID PRIMARY KEY,
    filename TEXT NOT NULL,
    mime_type TEXT NOT NULL,
    audio_data BYTEA NOT NULL,
    audio_size BIGINT NOT NULL CHECK (audio_size >= 0),
    status TEXT NOT NULL DEFAULT 'uploaded'
        CHECK (status IN ('uploaded', 'processing', 'completed', 'failed')),
    processing_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS transcripts (
    recording_id UUID PRIMARY KEY REFERENCES recordings(id) ON DELETE CASCADE,
    full_text TEXT NOT NULL DEFAULT '',
    segments JSONB NOT NULL DEFAULT '[]'::jsonb,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS summaries (
    recording_id UUID PRIMARY KEY REFERENCES recordings(id) ON DELETE CASCADE,
    summary_text TEXT NOT NULL DEFAULT '',
    key_points JSONB NOT NULL DEFAULT '[]'::jsonb,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_recordings_created_at ON recordings(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_recordings_status ON recordings(status);

ALTER TABLE recordings ADD COLUMN IF NOT EXISTS processing_stage TEXT NOT NULL DEFAULT '';
ALTER TABLE recordings ADD COLUMN IF NOT EXISTS title TEXT NOT NULL DEFAULT '';
ALTER TABLE transcripts ADD COLUMN IF NOT EXISTS diarization_status TEXT NOT NULL DEFAULT 'unavailable';

CREATE TABLE IF NOT EXISTS processing_jobs (
    recording_id UUID PRIMARY KEY REFERENCES recordings(id) ON DELETE CASCADE,
    mode TEXT NOT NULL CHECK (mode IN ('resume', 'full', 'summary')),
    state TEXT NOT NULL DEFAULT 'queued' CHECK (state IN ('queued', 'running', 'done', 'failed')),
    lease_token UUID,
    lease_until TIMESTAMPTZ,
    attempts INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_jobs_pending ON processing_jobs(state, lease_until, created_at);

-- Retained for migration from the previous single-owner version.
CREATE TABLE IF NOT EXISTS auth_owner (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    username TEXT NOT NULL,
    password_hash BYTEA NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE IF NOT EXISTS auth_sessions (
    token_hash BYTEA PRIMARY KEY,
    owner_id INTEGER NOT NULL REFERENCES auth_owner(id) ON DELETE CASCADE,
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_auth_sessions_expiry ON auth_sessions(expires_at);

CREATE TABLE IF NOT EXISTS auth_users (
    id UUID PRIMARY KEY,
    username TEXT NOT NULL,
    password_hash BYTEA NOT NULL,
    legacy_owner BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_username ON auth_users (lower(username));
CREATE TABLE IF NOT EXISTS auth_user_sessions (
    token_hash BYTEA PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES auth_users(id) ON DELETE CASCADE,
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_user_sessions_expiry ON auth_user_sessions(expires_at);
ALTER TABLE recordings ADD COLUMN IF NOT EXISTS user_id UUID REFERENCES auth_users(id);
CREATE INDEX IF NOT EXISTS idx_recordings_user_created ON recordings(user_id, created_at DESC);
CREATE TABLE IF NOT EXISTS auth_migrations (name TEXT PRIMARY KEY);
-- Run once: retained legacy tables must never resurrect a revoked session.
DO $$ BEGIN
    IF NOT EXISTS (SELECT 1 FROM auth_migrations WHERE name='multiple-users-v1') THEN
        INSERT INTO auth_users(id, username, password_hash, legacy_owner, created_at)
        SELECT '00000000-0000-4000-8000-000000000001'::uuid, username, password_hash, true, created_at
        FROM auth_owner ON CONFLICT DO NOTHING;
        INSERT INTO auth_user_sessions(token_hash, user_id, expires_at)
        SELECT token_hash, '00000000-0000-4000-8000-000000000001'::uuid, expires_at
        FROM auth_sessions WHERE expires_at>NOW()
        AND EXISTS (SELECT 1 FROM auth_users WHERE id='00000000-0000-4000-8000-000000000001')
        ON CONFLICT DO NOTHING;
        UPDATE recordings SET user_id='00000000-0000-4000-8000-000000000001'
        WHERE user_id IS NULL AND EXISTS (SELECT 1 FROM auth_users WHERE id='00000000-0000-4000-8000-000000000001');
        INSERT INTO auth_migrations(name) VALUES ('multiple-users-v1');
    END IF;
END $$;

-- Recover jobs left by versions that ran processing only in memory.
INSERT INTO processing_jobs (recording_id, mode)
SELECT id, 'resume' FROM recordings WHERE status = 'processing'
ON CONFLICT (recording_id) DO NOTHING;
UPDATE recordings SET processing_stage = 'queued', updated_at = NOW()
WHERE status = 'processing' AND processing_stage = '';
`
	_, err := pool.Exec(ctx, schema)
	if err != nil {
		return fmt.Errorf("execute schema: %w", err)
	}
	return nil
}
