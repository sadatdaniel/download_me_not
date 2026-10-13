package api

import (
	"context"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/sadatdaniel/download_me_not/internal/cookies"
)

// Cookies are session credentials, kept per user: each user's cookies apply only to their own downloads.
// Values are never returned.
func (s *server) cookieRoutes(api huma.API) {
	mine := func(ctx context.Context) *cookies.Store { return s.Cookies.User(userOf(ctx)) }
	huma.Register(api, op("list-cookies", "GET", "/v1/cookies", "read", "Domains you have imported cookies for (never the values)"),
		func(ctx context.Context, _ *struct{}) (*struct{ Body []cookies.Summary }, error) {
			c := mine(ctx)
			if c == nil {
				return body([]cookies.Summary{}), nil
			}
			l, err := c.List()
			return body(l), err
		})
	huma.Register(api, op("import-cookies", "POST", "/v1/cookies", "write",
		"Import a Netscape cookies.txt for one domain (and its subdomains), for your own downloads; replaces that domain's cookies"),
		func(ctx context.Context, in *struct {
			Body struct {
				Domain    string `json:"domain" minLength:"3"`
				Content   string `json:"content" minLength:"1" maxLength:"1048576" doc:"cookies.txt text"`
				UserAgent string `json:"userAgent,omitempty" doc:"the browser's User-Agent, sent with these cookies"`
			}
		}) (*struct{ Body map[string]int }, error) {
			c := mine(ctx)
			if c == nil {
				return nil, huma.Error503ServiceUnavailable("cookie store unavailable")
			}
			n, err := c.Import(in.Body.Domain, strings.NewReader(in.Body.Content), in.Body.UserAgent)
			if err != nil {
				return nil, huma.Error400BadRequest(err.Error())
			}
			return body(map[string]int{"imported": n}), nil
		})
	huma.Register(api, op("delete-cookies", "DELETE", "/v1/cookies/{domain}", "write", "Forget your cookies for a domain"),
		func(ctx context.Context, in *struct {
			Domain string `path:"domain"`
		}) (*struct{}, error) {
			c := mine(ctx)
			if c == nil {
				return nil, huma.Error404NotFound("no cookies")
			}
			if err := c.Delete(in.Domain); err != nil {
				return nil, huma.Error404NotFound(err.Error())
			}
			return nil, nil
		})
}
