package api

import (
	"net/http/httptest"
	"testing"

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

func TestSessionTokenIsStableForRequestRetry(t *testing.T) {
	raw, hash := sessionToken("password-hash", "request-id")
	retriedRaw, retriedHash := sessionToken("password-hash", "request-id")
	otherRaw, _ := sessionToken("password-hash", "other-request")
	require.Equal(t, raw, retriedRaw)
	require.Equal(t, hash, retriedHash)
	require.NotEqual(t, raw, otherRaw)
}

func TestSessionCookieAcceptsNormalizedUsername(t *testing.T) {
	request := httptest.NewRequest("GET", "/", nil)
	request.Header.Set("Cookie", "wordflow_session=mjones.token")
	username, token, err := sessionCookie(request)
	require.NoError(t, err)
	require.Equal(t, "mjones", username)
	require.Equal(t, "token", token)
}
