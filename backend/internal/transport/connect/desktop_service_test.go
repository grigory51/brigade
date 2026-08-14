package connectsvc

import (
	"testing"

	"github.com/grigory51/brigade/backend/internal/desktopenv"
)

func TestDesktopEnvironmentDoesNotInventPasswordLogin(t *testing.T) {
	environment := desktopEnvironmentToProto(desktopenv.Environment{ID: "remote", Kind: "remote"}, "remote")
	if len(environment.AuthMethods) != 0 {
		t.Fatalf("auth methods = %+v", environment.AuthMethods)
	}
}
