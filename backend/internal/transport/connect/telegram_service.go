package connectsvc

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	v1 "github.com/grigory51/brigade/backend/gen/go/brigade/v1"
	"github.com/grigory51/brigade/backend/internal/store"
	telegramsvc "github.com/grigory51/brigade/backend/internal/telegram"
)

type TelegramService struct {
	telegram *telegramsvc.Service
}

func NewTelegramService(service *telegramsvc.Service) *TelegramService {
	return &TelegramService{telegram: service}
}

func (s *TelegramService) ListBots(ctx context.Context, _ *connect.Request[v1.Empty]) (*connect.Response[v1.ListTelegramBotsResponse], error) {
	userID, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	bots, err := s.telegram.List(ctx, userID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	out := make([]*v1.TelegramBot, 0, len(bots))
	for _, bot := range bots {
		out = append(out, telegramBotToProto(bot))
	}
	return connect.NewResponse(&v1.ListTelegramBotsResponse{Bots: out, Mode: s.telegram.Mode()}), nil
}

func (s *TelegramService) SaveBot(ctx context.Context, req *connect.Request[v1.SaveTelegramBotRequest]) (*connect.Response[v1.TelegramBot], error) {
	userID, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	if req.Msg.Bot == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("telegram bot required"))
	}
	in := req.Msg.Bot
	saved, err := s.telegram.Save(ctx, userID, store.TelegramBot{
		ID: in.Id, Token: req.Msg.Token, AgentType: in.AgentType, AuthProfile: in.AuthProfile,
		Image: in.Image, McpServers: in.McpServerIds,
	})
	if err != nil {
		code := connect.CodeInvalidArgument
		if errors.Is(err, store.ErrNotFound) {
			code = connect.CodeNotFound
		}
		return nil, connect.NewError(code, err)
	}
	return connect.NewResponse(telegramBotToProto(saved)), nil
}

func (s *TelegramService) DeleteBot(ctx context.Context, req *connect.Request[v1.TelegramBotRequest]) (*connect.Response[v1.Empty], error) {
	userID, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.telegram.Delete(ctx, userID, req.Msg.Id); err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	return connect.NewResponse(&v1.Empty{}), nil
}

func (s *TelegramService) CreateBindingLink(ctx context.Context, req *connect.Request[v1.TelegramBotRequest]) (*connect.Response[v1.TelegramBindingLink], error) {
	userID, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	url, expires, err := s.telegram.BindingLink(ctx, userID, req.Msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	return connect.NewResponse(&v1.TelegramBindingLink{Url: url, ExpiresAt: expires.Unix()}), nil
}

func telegramBotToProto(bot store.TelegramBot) *v1.TelegramBot {
	return &v1.TelegramBot{
		Id: bot.ID, Username: bot.Username, Name: bot.Name, TokenSet: bot.Token != "",
		OwnerConnected: bot.OwnerTelegramID != 0, OwnerUsername: bot.OwnerTelegramUsername,
		AgentType: bot.AgentType, AuthProfile: bot.AuthProfile, Image: bot.Image,
		McpServerIds: bot.McpServers, SupportsGuestQueries: bot.SupportsGuestQueries,
		HasTopicsEnabled: bot.HasTopicsEnabled,
	}
}
