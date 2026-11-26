package connectsvc

import (
	"errors"
	"testing"

	"connectrpc.com/connect"

	"github.com/grigory51/brigade/backend/internal/coding"
)

func TestCodingErrorWorkspaceReasons(t *testing.T) {
	tests := []struct {
		err    error
		code   connect.Code
		reason string
		wrap   bool
		retry  string
	}{
		{coding.ErrNoRemote, connect.CodeFailedPrecondition, "no-remote", true, ""},
		{coding.ErrNoHEAD, connect.CodeFailedPrecondition, "no-head", true, ""},
		{coding.ErrUnsupportedWorkspace, connect.CodeFailedPrecondition, "unsupported-workspace", true, ""},
		{coding.ErrWorkspace, connect.CodeFailedPrecondition, "workspace-unavailable", false, ""},
		{coding.ErrNotConfigured, connect.CodeFailedPrecondition, "not-configured", false, ""},
		{coding.ErrRateLimited, connect.CodeResourceExhausted, "rate-limited", false, "15"},
	}
	for _, test := range tests {
		t.Run(test.reason, func(t *testing.T) {
			err := test.err
			if test.wrap {
				err = errors.Join(coding.ErrWorkspace, err)
			}
			rpcErr := codingError(err)
			if rpcErr.Code() != test.code || rpcErr.Meta().Get("coding-reason") != test.reason || rpcErr.Meta().Get("retry-after") != test.retry {
				t.Fatalf("unexpected Connect error: code=%s reason=%q retry-after=%q", rpcErr.Code(), rpcErr.Meta().Get("coding-reason"), rpcErr.Meta().Get("retry-after"))
			}
		})
	}
}
