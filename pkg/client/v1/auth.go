package permissions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"

	"go.infratographer.com/x/versionx"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

var (
	tracer = otel.Tracer("go.infratographer.com/permissions-api/pkg/permissions/v1")

	apiVersion = "/api/v1"
)

// Doer is an interface for an HTTP client that can make requests
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

type Client struct {
	url        string
	httpClient Doer
	authToken  string
}

func New(url string, doerClient Doer) (*Client, error) {
	if url == "" {
		return nil, ErrMissingURI
	}

	url = strings.TrimSuffix(url, "/")

	c := &Client{
		url: url,
	}

	c.httpClient = doerClient
	if c.httpClient == nil {
		// Use the default client as a fallback if one isn't passed
		c.httpClient = &http.Client{
			Transport: otelhttp.NewTransport(http.DefaultTransport),
		}
	}

	return c, nil
}

func (c *Client) ResourcesAvailable(ctx context.Context, authToken string, resourceURNPrefix string, scope string) ([]string, error) {
	ctx, span := tracer.Start(ctx, "ResourcesAvailable")
	defer span.End()

	resp := map[string][]string{}

	err := c.withToken(authToken).get(ctx, fmt.Sprintf("available/%s/%s", resourceURNPrefix, scope), &resp)
	if err != nil {
		return []string{}, err
	}

	return resp["ids"], nil
}

func (c *Client) ActorHasScope(ctx context.Context, authToken string, scope string, resourceURNPrefix string) (bool, error) {
	ctx, span := tracer.Start(ctx, "ActorHasScope", trace.WithAttributes(
		attribute.String("scope", scope),
		attribute.String("resource", resourceURNPrefix),
	))
	defer span.End()

	err := c.withToken(authToken).get(ctx, fmt.Sprintf("/has/%s/on/%s", scope, resourceURNPrefix), map[string]string{})
	if err != nil {
		if errors.Is(err, ErrPermissionDenied) {
			return false, nil
		}

		return false, err
	}

	return true, nil
}

func (c *Client) ActorHasGlobalScope(ctx context.Context, authToken string, scope string) (bool, error) {
	ctx, span := tracer.Start(ctx, "ActorHasGlobalScope",
		trace.WithAttributes(attribute.String("scope", scope)),
	)
	defer span.End()

	err := c.withToken(authToken).get(ctx, fmt.Sprintf("/global/check/%s", scope), map[string]string{})
	if err != nil {
		if errors.Is(err, ErrPermissionDenied) {
			return false, nil
		}

		return false, err
	}

	return true, nil
}

// ServerResponse represents the data that the server will return on any given call
type ServerResponse struct {
	Message    string `json:"message,omitempty"`
	Error      string `json:"error,omitempty"`
	StatusCode int
}

func (c Client) get(ctx context.Context, endpoint string, resp interface{}) error {
	request, err := newGetRequest(ctx, c.url, endpoint)
	if err != nil {
		return err
	}

	return c.do(request, &resp)
}

func newGetRequest(ctx context.Context, uri, endpoint string) (*http.Request, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return nil, err
	}

	u.Path = path.Join(apiVersion, endpoint)

	return http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
}

func userAgentString() string {
	return fmt.Sprintf("%s (%s)", versionx.BuildDetails().AppName, versionx.BuildDetails().Version)
}

func (c Client) withToken(authToken string) Client {
	c.authToken = authToken
	return c
}

func (c Client) do(req *http.Request, result interface{}) error {
	if c.authToken == "" {
		return ErrNoAuthToken
	}

	req.Header.Set("Authorization", fmt.Sprintf("bearer %s", c.authToken))
	req.Header.Set("User-Agent", userAgentString())

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}

	if err := ensureValidServerResponse(resp); err != nil {
		return err
	}

	defer resp.Body.Close()

	return json.NewDecoder(resp.Body).Decode(&result)
}

func ensureValidServerResponse(resp *http.Response) error {
	if resp.StatusCode >= http.StatusMultiStatus {
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusForbidden {
			return ErrPermissionDenied
		}

		return errors.New("bad response from server")
	}

	return nil
}
