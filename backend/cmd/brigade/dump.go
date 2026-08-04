package main

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime/debug"
	"time"

	"github.com/spf13/cobra"

	v1 "github.com/grigory51/brigade/backend/gen/go/brigade/v1"
	"github.com/grigory51/brigade/backend/internal/acp"
	"github.com/grigory51/brigade/backend/internal/agentauth"
	"github.com/grigory51/brigade/backend/internal/agui"
	"github.com/grigory51/brigade/backend/internal/config"
	"github.com/grigory51/brigade/backend/internal/daemonrpc"
	"github.com/grigory51/brigade/backend/internal/spawn"
	"github.com/grigory51/brigade/backend/internal/store"
)

const debugEventLimit = 200
const debugMessageLimit = 20
const debugPayloadLimit = 4096

type sessionDebugDump struct {
	Version        string                   `json:"version"`
	Revision       string                   `json:"revision,omitempty"`
	BuildModified  bool                     `json:"buildModified,omitempty"`
	DumpedAt       time.Time                `json:"dumpedAt"`
	Session        store.Session            `json:"session"`
	Daemon         daemonDebugDump          `json:"daemon"`
	Container      *spawn.ACPContainerDebug `json:"container,omitempty"`
	ContainerError string                   `json:"containerError,omitempty"`
	MessageCount   int                      `json:"messageCount"`
	Messages       []debugMessage           `json:"messages,omitempty"`
	Events         []debugEvent             `json:"events,omitempty"`
	LiveEvents     []debugEvent             `json:"liveEvents,omitempty"`
}

type daemonDebugDump struct {
	Address            string `json:"address,omitempty"`
	Generating         bool   `json:"generating"`
	Seq                int64  `json:"seq"`
	PendingPermissions int    `json:"pendingPermissions"`
	Error              string `json:"error,omitempty"`
	MessagesError      string `json:"messagesError,omitempty"`
	EventsError        string `json:"eventsError,omitempty"`
}

type debugMessage struct {
	ID            string `json:"id"`
	Role          string `json:"role"`
	ContentLength int    `json:"contentLength,omitempty"`
	ToolName      string `json:"toolName,omitempty"`
	ArgsLength    int    `json:"argsLength,omitempty"`
	ResultLength  int    `json:"resultLength,omitempty"`
}

type debugEvent struct {
	Seq             int64          `json:"seq"`
	Type            agui.EventType `json:"type"`
	MessageID       string         `json:"messageId,omitempty"`
	ToolCallID      string         `json:"toolCallId,omitempty"`
	ToolCallName    string         `json:"toolCallName,omitempty"`
	Name            string         `json:"name,omitempty"`
	ParentMessageID string         `json:"parentMessageId,omitempty"`
	DeltaLength     int            `json:"deltaLength,omitempty"`
	ContentLength   int            `json:"contentLength,omitempty"`
	Delta           string         `json:"delta,omitempty"`
	Content         string         `json:"content,omitempty"`
	Message         string         `json:"message,omitempty"`
	Value           string         `json:"value,omitempty"`
}

func newDumpCommand(configPath *string) *cobra.Command {
	session := &cobra.Command{
		Use:   "session <id>",
		Short: "вывести ограниченный диагностический снимок сессии в JSON",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return dumpDebugSession(cmd.Context(), cmd, *configPath, args[0])
		},
	}
	debug := &cobra.Command{Use: "debug", Short: "диагностические дампы"}
	debug.AddCommand(session)
	dump := &cobra.Command{Use: "dump", Short: "выгрузить данные brigade"}
	dump.AddCommand(debug)
	return dump
}

func dumpDebugSession(ctx context.Context, cmd *cobra.Command, configPath, sessionID string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	st, err := store.OpenReadOnly(cfg.SQLitePath)
	if err != nil {
		return err
	}
	defer st.Close()

	sess, err := st.GetSession(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("dump session %s: %w", sessionID, err)
	}
	out := sessionDebugDump{Version: buildVersion, DumpedAt: time.Now(), Session: sess}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				out.Revision = setting.Value
			case "vcs.modified":
				out.BuildModified = setting.Value == "true"
			}
		}
	}
	if sess.Mode == store.SessionModeDocker && sess.Kind == store.SessionKindACP {
		dumpDockerACP(ctx, cfg, sessionID, &out)
	} else {
		out.Daemon.Error = "live diagnostics are available for docker ACP sessions only"
	}

	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func dumpDockerACP(ctx context.Context, cfg *config.Config, sessionID string, out *sessionDebugDump) {
	docker, err := spawn.NewDockerSpawner(cfg.AgentImage)
	if err != nil {
		out.Daemon.Error = err.Error()
		return
	}
	defer docker.Close()
	if containerDump, err := docker.ACP().DebugContainer(ctx, sessionID); err != nil {
		out.ContainerError = err.Error()
	} else {
		out.Container = &containerDump
	}
	addr, ok := docker.ACP().DaemonAddr(ctx, sessionID)
	if !ok {
		out.Daemon.Error = "agent daemon container is unavailable"
		return
	}
	out.Daemon.Address = addr

	signer := agentauth.NewSigner(cfg.JWT.Secret)
	conn := daemonrpc.Dial(addr, "dump", func() (string, error) { return signer.Token(sessionID) })
	status, err := conn.RPC.Status(ctx, daemonrpc.Req(conn.Sign(), &v1.Empty{}))
	if err != nil {
		out.Daemon.Error = err.Error()
		return
	}
	out.Daemon.Generating = status.Msg.Generating
	out.Daemon.Seq = status.Msg.Seq
	out.Daemon.PendingPermissions = len(status.Msg.PendingPermissionsJson)

	messages, err := conn.RPC.GetMessages(ctx, daemonrpc.Req(conn.Sign(), &v1.Empty{}))
	if err != nil {
		out.Daemon.MessagesError = err.Error()
	} else {
		var list []acp.Message
		if err := json.Unmarshal(messages.Msg.Json, &list); err != nil {
			out.Daemon.MessagesError = err.Error()
		} else {
			out.MessageCount = len(list)
			for _, message := range list[max(0, len(list)-debugMessageLimit):] {
				out.Messages = append(out.Messages, debugMessage{
					ID: message.ID, Role: message.Role, ContentLength: len(message.Content),
					ToolName: message.ToolName, ArgsLength: len(message.ArgsText), ResultLength: len(message.Result),
				})
			}
		}
	}
	out.Events, out.LiveEvents, err = readDebugEvents(ctx, conn, status.Msg.Seq)
	if err != nil {
		out.Daemon.EventsError = err.Error()
	}
}

func readDebugEvents(ctx context.Context, conn daemonrpc.Conn, target int64) ([]debugEvent, []debugEvent, error) {
	from := max(int64(0), target-debugEventLimit)
	streamCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	stream, err := conn.RPC.StreamEvents(streamCtx, daemonrpc.Req(conn.Sign(), &v1.DaemonStreamEventsRequest{FromSeq: from}))
	if err != nil {
		return nil, nil, err
	}
	expected := from + 1
	var events, live []debugEvent
	for expected <= target && stream.Receive() {
		msg := stream.Msg()
		if msg.Seq != expected {
			if msg.Seq == target {
				if event, ok := summarizeDebugEvent(msg); ok {
					live = append(live, event)
				}
			}
			continue
		}
		if event, ok := summarizeDebugEvent(msg); ok {
			events = append(events, event)
		}
		expected++
	}
	if err := stream.Err(); err != nil && expected <= target {
		return events, live, err
	}
	return events, live, nil
}

func summarizeDebugEvent(message *v1.DaemonEvent) (debugEvent, bool) {
	var event agui.Event
	if json.Unmarshal(message.AguiJson, &event) != nil {
		return debugEvent{}, false
	}
	return debugEvent{
		Seq: message.Seq, Type: event.Type, MessageID: event.MessageID,
		ToolCallID: event.ToolCallID, ToolCallName: event.ToolCallName, Name: event.Name,
		ParentMessageID: event.ParentMessageID,
		DeltaLength:     len(event.Delta), ContentLength: len(event.Content),
		Delta: debugPreview(event.Delta), Content: debugPreview(event.Content),
		Message: debugPreview(event.Message), Value: debugJSONPreview(event.Value),
	}, true
}

func debugPreview(value string) string {
	if len(value) <= debugPayloadLimit {
		return value
	}
	return value[:debugPayloadLimit] + "…"
}

func debugJSONPreview(value any) string {
	if value == nil {
		return ""
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("<json error: %v>", err)
	}
	return debugPreview(string(raw))
}
