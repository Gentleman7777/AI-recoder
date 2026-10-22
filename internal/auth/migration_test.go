package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ai-recorder/internal/database"
	"ai-recorder/internal/model"
	"ai-recorder/internal/repository"
	"ai-recorder/internal/testutil"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

func TestLegacyAccountMigration(t *testing.T) {
	pool := testutil.Database(t)
	ctx := context.Background()
	hash, err := bcrypt.GenerateFromPassword([]byte("legacy-test-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, "INSERT INTO auth_owner(id,username,password_hash) VALUES(1,'Original',$1)", hash); err != nil {
		t.Fatal(err)
	}
	token := randomToken()
	if _, err = pool.Exec(ctx, "INSERT INTO auth_sessions(token_hash,owner_id,expires_at) VALUES($1,1,$2)", tokenHash(token), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	repo := repository.NewRecordingRepository(pool)
	id := uuid.NewString()
	if err = repo.Create(ctx, model.Recording{ID: id, Filename: "legacy.wav", MimeType: "audio/wav"}, []byte("legacy")); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, "DELETE FROM auth_migrations WHERE name='multiple-users-v1'"); err != nil {
		t.Fatal(err)
	}
	if err = database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var owner, name string
	var legacy bool
	if err = pool.QueryRow(ctx, "SELECT r.user_id::text,u.username,u.legacy_owner FROM recordings r JOIN auth_users u ON u.id=r.user_id WHERE r.id=$1", id).Scan(&owner, &name, &legacy); err != nil || name != "Original" || !legacy {
		t.Fatal("legacy owner or recording lost", err)
	}
	if _, err = repo.ForUser(owner).Get(ctx, id); err != nil {
		t.Fatal("migrated owner cannot open audio", err)
	}
	r := authRequest("GET", "/api/recordings", "", &http.Cookie{Name: "echo_local_session", Value: token})
	if user, err := (&Auth{pool: pool}).authenticated(r); err != nil || user.ID != owner {
		t.Fatal("legacy session lost", err)
	}
	if _, err = pool.Exec(ctx, "DELETE FROM auth_user_sessions"); err != nil {
		t.Fatal(err)
	}
	if err = database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var sessions int
	if err = pool.QueryRow(ctx, "SELECT COUNT(*) FROM auth_user_sessions").Scan(&sessions); err != nil || sessions != 0 {
		t.Fatal("migration resurrected a revoked session")
	}
}

func TestLegacyAudioNotClaimedByPublicRegistration(t *testing.T) {
	pool := testutil.Database(t)
	repo := repository.NewRecordingRepository(pool)
	ctx := context.Background()
	id := uuid.NewString()
	if err := repo.Create(ctx, model.Recording{ID: id, Filename: "private-old.wav", MimeType: "audio/wav"}, []byte("private old audio")); err != nil {
		t.Fatal(err)
	}
	a := New(pool)
	register := func(local bool, name string) User {
		r := authRequest("POST", "/api/auth/register", `{"username":"`+name+`","password":"registration-test-password"}`, nil)
		if !local {
			r.Host = "echo.example"
			r.Header.Set("Origin", "https://echo.example")
		}
		w := httptest.NewRecorder()
		a.credentials(w, r, true)
		if w.Code != 200 {
			t.Fatalf("register: %d %s", w.Code, w.Body)
		}
		var user User
		if err := pool.QueryRow(ctx, "SELECT id::text,username,legacy_owner FROM auth_users WHERE username=$1", name).Scan(&user.ID, &user.Username, &user.LegacyOwner); err != nil {
			t.Fatal(err)
		}
		return user
	}
	remote := register(false, "Remote")
	if _, err := repo.ForUser(remote.ID).Get(ctx, id); !repository.IsNotFound(err) || remote.LegacyOwner {
		t.Fatal("public user claimed legacy audio")
	}
	local := register(true, "Local")
	if _, err := repo.ForUser(local.ID).Get(ctx, id); err != nil || !local.LegacyOwner {
		t.Fatal("local owner lost legacy audio")
	}
	second := register(true, "SecondLocal")
	if _, err := repo.ForUser(second.ID).Get(ctx, id); !repository.IsNotFound(err) || second.LegacyOwner {
		t.Fatal("second local user claimed legacy audio")
	}
	// Repository scoping with an absent user must never turn into worker access.
	if rows, err := repo.ForUser("").List(ctx, 100, 0); err != nil || len(rows) != 0 {
		t.Fatal("empty user scope is not closed")
	}
	if err := repo.ForUser("").Create(ctx, model.Recording{ID: uuid.NewString()}, []byte("x")); err == nil {
		t.Fatal("unowned API recording created")
	}
}
