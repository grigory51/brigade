package agui

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stubVerifier — заглушка TokenVerifier с фиксированным исходом.
type stubVerifier struct {
	userID string
	ok     bool
}

func (v stubVerifier) Verify(string) (string, bool) { return v.userID, v.ok }

// stubProvider — заглушка ClientProvider, отдающая заранее заданный Bindable.
type stubProvider struct {
	b  Bindable
	ok bool
}

func (p stubProvider) Bindable(string, string) (Bindable, bool) { return p.b, p.ok }

// doCancel прогоняет cancelHandler на заданных verifier/provider с телом body и
// возвращает статус ответа.
func doCancel(v TokenVerifier, p ClientProvider, body string) int {
	req := httptest.NewRequest(http.MethodPost, "/api/ag-ui/cancel", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer t")
	rec := httptest.NewRecorder()
	cancelHandler(v, p).ServeHTTP(rec, req)
	return rec.Code
}

func TestCancelHandler(t *testing.T) {
	okVerifier := stubVerifier{userID: "u1", ok: true}

	t.Run("нет токена → 401", func(t *testing.T) {
		if code := doCancel(stubVerifier{ok: false}, stubProvider{}, `{"threadId":"s1"}`); code != http.StatusUnauthorized {
			t.Errorf("код = %d, want 401", code)
		}
	})

	t.Run("нет threadId → 400", func(t *testing.T) {
		if code := doCancel(okVerifier, stubProvider{}, `{}`); code != http.StatusBadRequest {
			t.Errorf("код = %d, want 400", code)
		}
	})

	t.Run("неизвестная сессия → 404", func(t *testing.T) {
		if code := doCancel(okVerifier, stubProvider{ok: false}, `{"threadId":"s1"}`); code != http.StatusNotFound {
			t.Errorf("код = %d, want 404", code)
		}
	})

	t.Run("успех → 204 и делегирование в Bindable.Cancel", func(t *testing.T) {
		b := &fakeBindable{}
		if code := doCancel(okVerifier, stubProvider{b: b, ok: true}, `{"threadId":"s1"}`); code != http.StatusNoContent {
			t.Errorf("код = %d, want 204", code)
		}
		if !b.cancelCalled {
			t.Error("Cancel не вызван у Bindable")
		}
	})

	t.Run("ошибка Cancel → 502", func(t *testing.T) {
		b := &fakeBindable{cancelErr: errors.New("boom")}
		if code := doCancel(okVerifier, stubProvider{b: b, ok: true}, `{"threadId":"s1"}`); code != http.StatusBadGateway {
			t.Errorf("код = %d, want 502", code)
		}
	})
}
