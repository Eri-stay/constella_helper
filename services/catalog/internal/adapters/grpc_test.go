package adapters

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	pb "constella/api/catalogpb"
	"constella/internal/domain/catalog"
)

func grpcClient(t *testing.T, ix *Index, token string) pb.CatalogClient {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := NewGRPCServer(ix, token)
	go func() { _ = srv.Serve(lis) }() // returns when the server stops
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return pb.NewCatalogClient(conn)
}

func authed(token string) context.Context {
	return metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+token)
}

func TestGRPCStreamsStarsAndEdges(t *testing.T) {
	t.Parallel()
	c := grpcClient(t, activeIndex(t), "secret")

	stream, err := c.StreamStars(authed("secret"), &pb.StarsRequest{MagMax: 1})
	if err != nil {
		t.Fatal(err)
	}
	var hips []int64
	for {
		s, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		hips = append(hips, s.Hip)
	}
	// Sample stars with HIP and mag <= 1: Sirius (-1.44) and Betelgeuse (0.45); Sol has no HIP.
	if len(hips) != 2 || hips[0] != 27989 || hips[1] != 32349 {
		t.Errorf("stars: %v", hips)
	}

	edges, err := c.StreamEdges(authed("secret"), &pb.EdgesRequest{CultureId: catalog.IAUCultureID})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for {
		e, err := edges.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if e.HipA == 0 || e.HipB == 0 {
			t.Errorf("edge without stars: %v", e)
		}
		n++
	}
	if n != 2 {
		t.Errorf("sample culture has 2 edges, got %d", n)
	}
}

func TestGRPCErrors(t *testing.T) {
	t.Parallel()
	c := grpcClient(t, activeIndex(t), "secret")
	code := func(err error) codes.Code { return status.Code(err) }
	recvErr := func(s grpc.ServerStreamingClient[pb.Edge], err error) error {
		if err != nil {
			return err
		}
		_, err = s.Recv()
		return err
	}

	if _, err := func() (*pb.Star, error) {
		s, err := c.StreamStars(context.Background(), &pb.StarsRequest{}) // no token
		if err != nil {
			return nil, err
		}
		return s.Recv()
	}(); code(err) != codes.Unauthenticated {
		t.Errorf("no token: %v", err)
	}
	if err := recvErr(c.StreamEdges(authed("secret"), &pb.EdgesRequest{CultureId: "nope"})); code(err) != codes.NotFound {
		t.Errorf("unknown culture: %v", err)
	}
	if err := recvErr(c.StreamEdges(authed("secret"), &pb.EdgesRequest{})); code(err) != codes.InvalidArgument {
		t.Errorf("missing culture: %v", err)
	}

	empty := grpcClient(t, &Index{}, "")
	s, err := empty.StreamStars(context.Background(), &pb.StarsRequest{})
	if err == nil {
		_, err = s.Recv()
	}
	if code(err) != codes.FailedPrecondition {
		t.Errorf("no active catalog: %v", err)
	}
}
