package adapters

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	pb "constella/api/catalogpb"
)

// EachStar calls fn for every star with a Hipparcos number and mag <= magMax
// (magMax 0 means all), brightness unordered, by HIP. Reading stops at the
// first error from fn.
func (ix *Index) EachStar(ctx context.Context, magMax float64, fn func(Star) error) error {
	db, err := ix.handle(ctx)
	if err != nil {
		return err
	}
	rows, err := db.QueryContext(ctx, `SELECT `+starColumns+` FROM stars
		WHERE hip <> 0 AND (? = 0 OR mag <= ?) ORDER BY hip`, magMax, magMax)
	if err != nil {
		return fmt.Errorf("query stars: %w", err)
	}
	defer rows.Close() //nolint:errcheck // Err() below reports failures
	for rows.Next() {
		s, err := scanStar(rows)
		if err != nil {
			return err
		}
		if err := fn(s); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate stars: %w", err)
	}
	return nil
}

// EachEdge calls fn for every line segment of a culture.
func (ix *Index) EachEdge(ctx context.Context, culture string, fn func(figure string, a, b int64) error) error {
	db, err := ix.handle(ctx)
	if err != nil {
		return err
	}
	var known int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM cultures WHERE id = ?`, culture).Scan(&known); err != nil {
		return fmt.Errorf("find culture %s: %w", culture, err)
	}
	if known == 0 {
		return ErrNotFound
	}
	rows, err := db.QueryContext(ctx, `SELECT constellation_id, hip_a, hip_b FROM culture_edges
		WHERE culture_id = ? ORDER BY constellation_id, line, seq`, culture)
	if err != nil {
		return fmt.Errorf("query edges of %s: %w", culture, err)
	}
	defer rows.Close() //nolint:errcheck // Err() below reports failures
	for rows.Next() {
		var (
			fig  string
			a, b int64
		)
		if err := rows.Scan(&fig, &a, &b); err != nil {
			return fmt.Errorf("scan edge: %w", err)
		}
		if err := fn(fig, a, b); err != nil {
			return err
		}
	}
	return rows.Err()
}

// GRPC serves the catalog streams over gRPC.
type GRPC struct {
	pb.UnimplementedCatalogServer
	Index *Index
}

// StreamStars implements pb.CatalogServer.
func (g GRPC) StreamStars(req *pb.StarsRequest, stream grpc.ServerStreamingServer[pb.Star]) error {
	if req.GetMagMax() < 0 {
		return status.Error(codes.InvalidArgument, "mag_max must not be negative")
	}
	ctx := WithBuild(stream.Context(), req.GetBuildId())
	err := g.Index.EachStar(ctx, req.GetMagMax(), func(s Star) error {
		return stream.Send(&pb.Star{Id: s.ID, Hip: s.HIP, Name: s.Name, Con: s.Con, Ra: s.RA, Dec: s.Dec, Mag: s.Mag})
	})
	return grpcErr(err)
}

// StreamEdges implements pb.CatalogServer.
func (g GRPC) StreamEdges(req *pb.EdgesRequest, stream grpc.ServerStreamingServer[pb.Edge]) error {
	if req.GetCultureId() == "" {
		return status.Error(codes.InvalidArgument, "culture_id is required")
	}
	ctx := WithBuild(stream.Context(), req.GetBuildId())
	err := g.Index.EachEdge(ctx, req.GetCultureId(), func(fig string, a, b int64) error {
		return stream.Send(&pb.Edge{ConstellationId: fig, HipA: a, HipB: b})
	})
	return grpcErr(err)
}

// grpcErr maps domain errors to gRPC codes; errors from Send already are gRPC errors.
func grpcErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrNoActive):
		return status.Error(codes.FailedPrecondition, CodeNoCatalog)
	case errors.Is(err, ErrNotFound):
		return status.Error(codes.NotFound, "NOT_FOUND")
	}
	if _, ok := status.FromError(err); ok {
		return err
	}
	return status.Error(codes.Internal, "INTERNAL")
}

// NewGRPCServer returns a server with the Catalog service, tracing and bearer
// token authentication (the same token as the HTTP APIs; empty disables it).
func NewGRPCServer(ix *Index, token string) *grpc.Server {
	srv := grpc.NewServer(
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
		grpc.ChainUnaryInterceptor(func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
			if err := authorize(ctx, token); err != nil {
				return nil, err
			}
			return h(ctx, req)
		}),
		grpc.ChainStreamInterceptor(func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, h grpc.StreamHandler) error {
			if err := authorize(ss.Context(), token); err != nil {
				return err
			}
			return h(srv, ss)
		}),
	)
	pb.RegisterCatalogServer(srv, GRPC{Index: ix})
	return srv
}

func authorize(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	md, _ := metadata.FromIncomingContext(ctx)
	got := ""
	if v := md.Get("authorization"); len(v) > 0 {
		got = v[0]
	}
	if subtle.ConstantTimeCompare([]byte(got), []byte("Bearer "+token)) != 1 {
		return status.Error(codes.Unauthenticated, "UNAUTHORIZED")
	}
	return nil
}
