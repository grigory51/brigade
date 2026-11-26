package acpremote

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/grigory51/brigade/backend/gen/go/brigade/v1"
	"github.com/grigory51/brigade/backend/gen/go/brigade/v1/brigadev1connect"
	"github.com/grigory51/brigade/backend/internal/acp"
)

type experienceDaemon struct {
	brigadev1connect.UnimplementedAgentDaemonServiceHandler
	configure   func(*v1.DaemonConfigureRequest) (*v1.DaemonConfigureResponse, error)
	statusError error
	generating  bool
}

func (d *experienceDaemon) Configure(_ context.Context, request *connect.Request[v1.DaemonConfigureRequest]) (*connect.Response[v1.DaemonConfigureResponse], error) {
	response, err := d.configure(request.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(response), nil
}

func (d *experienceDaemon) Status(context.Context, *connect.Request[v1.Empty]) (*connect.Response[v1.DaemonStatusResponse], error) {
	if d.statusError != nil {
		return nil, d.statusError
	}
	return connect.NewResponse(&v1.DaemonStatusResponse{Generating: d.generating}), nil
}

func (d *experienceDaemon) client(t *testing.T) *Client {
	t.Helper()
	_, handler := brigadev1connect.NewAgentDaemonServiceHandler(d)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return New(server.URL, "native", func() (string, error) { return "", nil })
}

func TestReconfigureRequiresAcknowledgementAndSameNativeID(t *testing.T) {
	for _, scenario := range []string{"success", "old daemon", "different native ID", "failure", "rollback failure"} {
		t.Run(scenario, func(t *testing.T) {
			d := &experienceDaemon{configure: func(request *v1.DaemonConfigureRequest) (*v1.DaemonConfigureResponse, error) {
				if !request.Reconfigure || request.ResumeSessionId != "native" || len(request.ExperienceMcpJson) != 0 {
					t.Errorf("incorrect plain chat reconfigure: %+v", request)
				}
				response := &v1.DaemonConfigureResponse{SessionId: "native", Reconfigured: true}
				switch scenario {
				case "old daemon":
					response.Reconfigured = false
				case "different native ID":
					response.SessionId = "new"
				case "failure":
					return nil, connect.NewError(connect.CodeInternal, errors.New("prepare failed"))
				case "rollback failure":
					return nil, connect.NewError(connect.CodeAborted, errors.New("runtime rollback failed"))
				}
				return response, nil
			}}
			client := d.client(t)
			_, err := client.Configure(t.Context(), ConfigureOptions{ResumeSessionID: "native", Reconfigure: true})
			if errors.Is(err, acp.ErrSessionUnrecoverable) != (scenario == "rollback failure") {
				t.Fatalf("rollback outcome lost: %v", err)
			}
			if (err == nil) != (scenario == "success") || client.SessionID() != "native" {
				t.Fatalf("error=%v native ID=%q", err, client.SessionID())
			}
		})
	}
}

func TestRemoteIdleReservationFailsClosed(t *testing.T) {
	for _, scenario := range []string{"idle", "generating", "status unavailable"} {
		t.Run(scenario, func(t *testing.T) {
			d := &experienceDaemon{generating: scenario == "generating"}
			if scenario == "status unavailable" {
				d.statusError = connect.NewError(connect.CodeUnavailable, errors.New("offline"))
			}
			client := d.client(t)
			release, err := client.ReserveIdle(t.Context())
			if scenario == "idle" {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := client.ReserveIdle(t.Context()); !errors.Is(err, acp.ErrSessionBusy) {
					t.Fatalf("second reservation: %v", err)
				}
				release()
			} else if err == nil {
				release()
				t.Fatal("non-idle or unavailable daemon was reserved")
			}
			if !client.promptMu.TryLock() {
				t.Fatal("turn barrier leaked after reservation")
			}
			client.promptMu.Unlock()
		})
	}
}
