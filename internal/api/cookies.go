package api

import (
	"context"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/sadatdaniel/download_me_not/internal/cookies"
)

// Cookies are session credentials: importing and deleting need admin, and values are never returned.
func (s *server) cookieRoutes(api huma.API) {
	huma.Register(api, op("list-cookies", "GET", "/v1/cookies", "read", "Domains with imported cookies (never the values)"),
		func(ctx context.Context, _ *struct{}) (*struct{ Body []cookies.Summary }, error) {
			if s.Cookies == nil {
				return body([]cookies.Summary{}), nil
			}
			l, err := s.Cookies.List()
			return body(l), err
		})
	huma.Register(api, op("import-cookies", "POST", "/v1/cookies", "admin",
		"Import a Netscape cookies.txt for one domain (and its subdomains); replaces that domain's cookies"),
		func(ctx context.Context, in *struct {
			Body struct {
				Domain    string `json:"domain" minLength:"3"`
				Content   string `json:"content" minLength:"1" maxLength:"1048576" doc:"cookies.txt text"`
				UserAgent string `json:"userAgent,omitempty" doc:"the browser's User-Agent, sent with these cookies"`
			}
		}) (*struct{ Body map[string]int }, error) {
			if s.Cookies == nil {
				return nil, huma.Error503ServiceUnavailable("cookie store unavailable")
			}
			n, err := s.Cookies.Import(in.Body.Domain, strings.NewReader(in.Body.Content), in.Body.UserAgent)
			if err != nil {
				return nil, huma.Error400BadRequest(err.Error())
			}
			return body(map[string]int{"imported": n}), nil
		})
	huma.Register(api, op("delete-cookies", "DELETE", "/v1/cookies/{domain}", "admin", "Forget a domain's cookies"),
		func(ctx context.Context, in *struct {
			Domain string `path:"domain"`
		}) (*struct{}, error) {
			if s.Cookies == nil {
				return nil, huma.Error404NotFound("no cookies")
			}
			if err := s.Cookies.Delete(in.Domain); err != nil {
				return nil, huma.Error404NotFound(err.Error())
			}
			return nil, nil
		})
}
