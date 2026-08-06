package connectsvc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	v1 "github.com/grigory51/brigade/backend/gen/go/brigade/v1"
	"github.com/grigory51/brigade/backend/internal/store"
)

const defaultResponseProfileID = "default"

type ResponseProfileService struct{ store *store.Store }

func NewResponseProfileService(st *store.Store) *ResponseProfileService {
	return &ResponseProfileService{store: st}
}

func (s *ResponseProfileService) List(ctx context.Context, _ *connect.Request[v1.Empty]) (*connect.Response[v1.ListResponseProfilesResponse], error) {
	userID, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	profiles, err := s.store.ListResponseProfiles(ctx, userID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	out := []*v1.ResponseProfile{{Id: defaultResponseProfileID, Name: "Обычно", ReadOnly: true}}
	for _, profile := range profiles {
		out = append(out, responseProfileToProto(profile))
	}
	return connect.NewResponse(&v1.ListResponseProfilesResponse{Profiles: out}), nil
}

func (s *ResponseProfileService) Create(ctx context.Context, req *connect.Request[v1.CreateResponseProfileRequest]) (*connect.Response[v1.ResponseProfile], error) {
	userID, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	profile, err := validatedResponseProfile("", userID, req.Msg.Name, req.Msg.Instructions)
	if err != nil {
		return nil, err
	}
	profile.ID = uuid.NewString()
	profile.CreatedAt = time.Now()
	profile.UpdatedAt = profile.CreatedAt
	if err := s.store.CreateResponseProfile(ctx, profile); err != nil {
		return nil, responseProfileStoreError(err, profile.Name)
	}
	return connect.NewResponse(responseProfileToProto(profile)), nil
}

func (s *ResponseProfileService) Update(ctx context.Context, req *connect.Request[v1.UpdateResponseProfileRequest]) (*connect.Response[v1.ResponseProfile], error) {
	userID, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	if req.Msg.Id == defaultResponseProfileID || req.Msg.Id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("профиль «Обычно» нельзя изменить"))
	}
	profile, err := validatedResponseProfile(req.Msg.Id, userID, req.Msg.Name, req.Msg.Instructions)
	if err != nil {
		return nil, err
	}
	profile.UpdatedAt = time.Now()
	if err := s.store.UpdateResponseProfile(ctx, profile); err != nil {
		return nil, responseProfileStoreError(err, profile.Name)
	}
	profile, err = s.store.GetResponseProfile(ctx, profile.ID, userID)
	if err != nil {
		return nil, responseProfileStoreError(err, profile.Name)
	}
	return connect.NewResponse(responseProfileToProto(profile)), nil
}

func (s *ResponseProfileService) Delete(ctx context.Context, req *connect.Request[v1.DeleteResponseProfileRequest]) (*connect.Response[v1.Empty], error) {
	userID, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	if req.Msg.Id == defaultResponseProfileID || req.Msg.Id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("профиль «Обычно» нельзя удалить"))
	}
	if err := s.store.DeleteResponseProfile(ctx, req.Msg.Id, userID); err != nil {
		return nil, responseProfileStoreError(err, "")
	}
	return connect.NewResponse(&v1.Empty{}), nil
}

func validatedResponseProfile(id, userID, name, instructions string) (store.ResponseProfile, error) {
	name = strings.TrimSpace(name)
	instructions = strings.TrimSpace(instructions)
	if name == "" || len([]rune(name)) > 80 {
		return store.ResponseProfile{}, connect.NewError(connect.CodeInvalidArgument, errors.New("название профиля должно содержать от 1 до 80 символов"))
	}
	if strings.EqualFold(name, "Обычно") {
		return store.ResponseProfile{}, connect.NewError(connect.CodeInvalidArgument, errors.New("название «Обычно» зарезервировано"))
	}
	if instructions == "" || len([]rune(instructions)) > 2000 {
		return store.ResponseProfile{}, connect.NewError(connect.CodeInvalidArgument, errors.New("инструкции должны содержать от 1 до 2000 символов"))
	}
	return store.ResponseProfile{ID: id, UserID: userID, Name: name, Instructions: instructions}, nil
}

func responseProfileToProto(profile store.ResponseProfile) *v1.ResponseProfile {
	return &v1.ResponseProfile{Id: profile.ID, Name: profile.Name, Instructions: profile.Instructions,
		CreatedAt: profile.CreatedAt.Unix(), UpdatedAt: profile.UpdatedAt.Unix()}
}

func responseProfileStoreError(err error, name string) error {
	if errors.Is(err, store.ErrNotFound) {
		return connect.NewError(connect.CodeNotFound, err)
	}
	if strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("профиль с именем %q уже есть", name))
	}
	return connect.NewError(connect.CodeInternal, err)
}
