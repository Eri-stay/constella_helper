package adapters

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	pb "constella/api/catalogpb"
	"constella/internal/domain/genconst"
	"constella/internal/platform/httpx"
	"constella/services/research/internal/app"
)

// CatalogClient reads stars and culture edges from catalog over gRPC streams,
// and asks its REST API which build is active.
type CatalogClient struct {
	conn    *grpc.ClientConn
	client  pb.CatalogClient
	rest    *httpx.Client
	restURL string
	token   string
}

// NewCatalogClient connects lazily; grpcAddr is host:port, restURL the base URL of the HTTP API.
func NewCatalogClient(grpcAddr, restURL, token string, rest *httpx.Client) (*CatalogClient, error) {
	conn, err := grpc.NewClient(grpcAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithStatsHandler(otelgrpc.NewClientHandler()), // trace context travels with every call
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(16<<20)))
	if err != nil {
		return nil, fmt.Errorf("create grpc client %s: %w", grpcAddr, err)
	}
	return &CatalogClient{conn: conn, client: pb.NewCatalogClient(conn), rest: rest, restURL: restURL, token: token}, nil
}

// Close closes the connection.
func (c *CatalogClient) Close() error { return c.conn.Close() }

func (c *CatalogClient) auth(ctx context.Context) context.Context {
	if c.token == "" {
		return ctx
	}
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+c.token)
}

// ActiveBuild implements app.Catalog.
func (c *CatalogClient) ActiveBuild(ctx context.Context) (string, error) {
	var out struct {
		Active  bool   `json:"active"`
		BuildID string `json:"build_id"`
	}
	if err := c.rest.Do(ctx, httpx.Request{Method: http.MethodGet, URL: c.restURL + "/v1/summary", Out: &out}); err != nil {
		return "", err
	}
	if !out.Active {
		return "", errors.New("catalog has no validated build yet")
	}
	return out.BuildID, nil
}

// Stars implements app.Catalog: a server stream of stars, RA converted to degrees.
func (c *CatalogClient) Stars(ctx context.Context, buildID string, magMax float64) ([]genconst.Star, error) {
	stream, err := c.client.StreamStars(c.auth(ctx), &pb.StarsRequest{MagMax: magMax, BuildId: buildID})
	if err != nil {
		return nil, fmt.Errorf("open star stream: %w", err)
	}
	var out []genconst.Star
	for {
		s, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("receive star: %w", err)
		}
		out = append(out, genconst.Star{HIP: s.Hip, RA: s.Ra * 15, Dec: s.Dec, Mag: s.Mag})
	}
}

// Edges implements app.Catalog.
func (c *CatalogClient) Edges(ctx context.Context, buildID, culture string) ([]genconst.Edge, error) {
	stream, err := c.client.StreamEdges(c.auth(ctx), &pb.EdgesRequest{CultureId: culture, BuildId: buildID})
	if err != nil {
		return nil, fmt.Errorf("open edge stream: %w", err)
	}
	var out []genconst.Edge
	for {
		e, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if status.Code(err) == codes.NotFound {
			return nil, app.ErrNoCulture
		}
		if err != nil {
			return nil, fmt.Errorf("receive edge: %w", err)
		}
		out = append(out, genconst.Edge{A: e.HipA, B: e.HipB})
	}
}
