package api

import (
	"context"
	"time"

	"github.com/danielgtaylor/huma/v2"
)

func (s *server) toolRoutes(api huma.API) {
	if s.Tools == nil {
		return
	}
	for _, a := range []string{"install", "rollback"} {
		huma.Register(api, op("tool-"+a, "POST", "/v1/tools/{name}/"+a, "admin",
			map[string]string{"install": "Install or update a managed tool (yt-dlp, rclone, deno) from its official release, checksum-verified",
				"rollback": "Put back the previous copy of a managed tool"}[a]),
			func(ctx context.Context, in *struct {
				Name string `path:"name"`
			}) (*struct{ Body map[string]string }, error) {
				if a == "rollback" {
					if err := s.Tools.Rollback(in.Name); err != nil {
						return nil, huma.Error400BadRequest(err.Error())
					}
					return body(map[string]string{"version": s.Tools.Installed(in.Name)}), nil
				}
				ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
				defer cancel()
				tag, err := s.Tools.Install(ctx, in.Name)
				if err != nil {
					return nil, huma.Error502BadGateway(err.Error())
				}
				return body(map[string]string{"version": tag}), nil
			})
	}
}
