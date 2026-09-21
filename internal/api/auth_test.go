package api

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPasswordHashIsStableForUsernameAndPassword(t *testing.T) {
	hash, err := passwordHash("temporal_user", "durable-password")
	require.NoError(t, err)
	same, err := passwordHash("temporal_user", "durable-password")
	require.NoError(t, err)
	wrongPassword, err := passwordHash("temporal_user", "wrong-password")
	require.NoError(t, err)
	otherUser, err := passwordHash("other_user", "durable-password")
	require.NoError(t, err)
	require.Equal(t, hash, same)
	require.NotEqual(t, hash, wrongPassword)
	require.NotEqual(t, hash, otherUser)
}

func TestUsernameIsNormalizedForHumanReadableWorkflowIDs(t *testing.T) {
	username, err := normalizeUsername("  Temporal_User ")
	require.NoError(t, err)
	require.Equal(t, "temporal_user", username)
}

func TestDisplayNameGetsStableUsernameDiscriminator(t *testing.T) {
	name := taggedDisplayName("michael", "Michael")
	require.Regexp(t, `^Michael#[0-9a-f]{8}$`, name)
	require.Equal(t, name, taggedDisplayName("michael", "Michael"))
	require.NotEqual(t, name, taggedDisplayName("another-user", "Michael"))
}

func TestSessionCookieVerifiesSignedJWT(t *testing.T) {
	secret := []byte("a-test-session-secret-with-at-least-32-characters")
	now := time.Date(2026, time.September, 21, 12, 0, 0, 0, time.UTC)
	token, err := newSessionToken(secret, "mjones", now, now.Add(time.Hour))
	require.NoError(t, err)

	request := httptest.NewRequest("GET", "/", nil)
	request.Header.Set("Cookie", "wordflow_session="+token)
	username, err := sessionCookie(request, secret, now)
	require.NoError(t, err)
	require.Equal(t, "mjones", username)

	_, err = sessionCookie(request, []byte("another-test-session-secret-at-least-32-characters"), now)
	require.ErrorContains(t, err, "invalid session")
	_, err = sessionCookie(request, secret, now.Add(time.Hour))
	require.ErrorContains(t, err, "invalid session")
}
