package connectsvc

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	v1 "github.com/grigory51/brigade/backend/gen/go/brigade/v1"
	"github.com/grigory51/brigade/backend/internal/linkpreview"
)

type LinkPreviewService struct {
	previews *linkpreview.Service
}

func NewLinkPreviewService(previews *linkpreview.Service) *LinkPreviewService {
	return &LinkPreviewService{previews: previews}
}

func (s *LinkPreviewService) Get(ctx context.Context, req *connect.Request[v1.LinkPreviewRequest]) (*connect.Response[v1.LinkPreviewResponse], error) {
	if _, err := requireUser(ctx); err != nil {
		return nil, err
	}
	preview, err := s.previews.Get(ctx, req.Msg.Url)
	if errors.Is(err, linkpreview.ErrInvalidURL) {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	return connect.NewResponse(&v1.LinkPreviewResponse{
		Url: preview.URL, Title: preview.Title, Description: preview.Description,
		ImageUrl: preview.ImageURL, SiteName: preview.SiteName, IconUrl: preview.IconURL,
	}), nil
}
