package unsubscribe

import "testing"

// Вектор посчитан независимо (Python: hmac + base64url) и продублирован в
// notrecinema-api/internal/notifications: тесты обоих модулей сверяют одно и
// то же значение.
const (
	vectorSecret = "test-secret"
	vectorUser   = "11111111-1111-1111-1111-111111111111"
	vectorToken  = "MTExMTExMTEtMTExMS0xMTExLTExMTEtMTExMTExMTExMTExfHNlcmllc19hZGRlZA.34QkSEz_HIOldM-BIyzSm6ETHnQpkdg0R1OJfkY1rJk"
)

func TestSignMatchesTheSharedVector(t *testing.T) {
	if got := Sign([]byte(vectorSecret), vectorUser, "series_added"); got != vectorToken {
		t.Errorf("Sign() = %q, want %q", got, vectorToken)
	}
}

func TestSignDependsOnEveryInput(t *testing.T) {
	base := Sign([]byte("a"), "user", "series_added")
	for name, other := range map[string]string{
		"secret":   Sign([]byte("b"), "user", "series_added"),
		"user":     Sign([]byte("a"), "other", "series_added"),
		"category": Sign([]byte("a"), "user", "poll"),
	} {
		if other == base {
			t.Errorf("changing the %s did not change the token", name)
		}
	}
}
