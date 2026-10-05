package relay

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
)

func TestPreviewCookiesCannotReplaceOrExpireStableSession(t *testing.T) {
	old := config.ReleaseChannel
	config.ReleaseChannel = "preview"
	t.Cleanup(func() { config.ReleaseChannel = old })
	s := NewServer(nil, ServerConfig{BaseURL: "http://localhost:8180"})
	recorder := httptest.NewRecorder()
	s.setSessionCookie(recorder, "preview-nonsecret-fixture")
	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "wt_preview_session" {
		t.Fatalf("preview set cookie = %v", cookies)
	}
	request := httptest.NewRequest(http.MethodGet, "http://localhost:8180/auth/logout", nil)
	request.AddCookie(&http.Cookie{Name: "wt_session", Value: "stable-nonsecret-fixture"})
	recorder = httptest.NewRecorder()
	s.handleLogout(recorder, request)
	cookies = recorder.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "wt_preview_session" || cookies[0].MaxAge >= 0 {
		t.Fatal("preview logout touched stable cookie", cookies)
	}
	config.ReleaseChannel = "stable"
	if sessionCookieNameForChannel() != "wt_session" {
		t.Fatal("stable cookie contract changed")
	}
}
