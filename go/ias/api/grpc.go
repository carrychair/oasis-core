package api

import (
	"context"

	"google.golang.org/grpc"

	cmnGrpc "github.com/oasisprotocol/oasis-core/go/common/grpc"
	"github.com/oasisprotocol/oasis-core/go/common/sgx/ias"
)

var (
	// serviceName is the gRPC service name.
	serviceName = cmnGrpc.NewServiceName("IAS")

	// methodVerifyEvidence is the VerifyEvidence method.
	methodVerifyEvidence = serviceName.NewMethod("VerifyEvidence", Evidence{})
	// methodGetSPIDInfo is the GetSPIDInfo method.
	methodGetSPIDInfo = serviceName.NewMethod("GetSPIDInfo", nil)
	// methodGetSigRL is the GetSigRL method.
	methodGetSigRL = serviceName.NewMethod("GetSigRL", uint32(0))

	// serviceDesc is the gRPC service descriptor.
	serviceDesc = grpc.ServiceDesc{
		ServiceName: string(serviceName),
		HandlerType: (*Endpoint)(nil),
		Methods: []grpc.MethodDesc{
			{
				MethodName: methodVerifyEvidence.ShortName(),
				Handler:    handlerVerifyEvidence,
			},
			{
				MethodName: methodGetSPIDInfo.ShortName(),
				Handler:    handlerGetSPIDInfo,
			},
			{
				MethodName: methodGetSigRL.ShortName(),
				Handler:    handlerGetSigRL,
			},
		},
		Streams: []grpc.StreamDesc{},
	}
)

func handlerVerifyEvidence(
	srv any,
	ctx context.Context,
	dec func(any) error,
	interceptor grpc.UnaryServerInterceptor,
) (any, error) {
	var req Evidence
	if err := dec(&req); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(Endpoint).VerifyEvidence(ctx, &req)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: methodVerifyEvidence.FullName(),
	}
	handler := func(ctx context.Context, req any) (any, error) {
		return srv.(Endpoint).VerifyEvidence(ctx, req.(*Evidence))
	}
	return interceptor(ctx, &req, info, handler)
}

func handlerGetSPIDInfo(
	srv any,
	ctx context.Context,
	_ func(any) error,
	interceptor grpc.UnaryServerInterceptor,
) (any, error) {
	if interceptor == nil {
		return srv.(Endpoint).GetSPIDInfo(ctx)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: methodGetSPIDInfo.FullName(),
	}
	handler := func(ctx context.Context, _ any) (any, error) {
		return srv.(Endpoint).GetSPIDInfo(ctx)
	}
	return interceptor(ctx, nil, info, handler)
}

func handlerGetSigRL(
	srv any,
	ctx context.Context,
	dec func(any) error,
	interceptor grpc.UnaryServerInterceptor,
) (any, error) {
	var epidGID uint32
	if err := dec(&epidGID); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(Endpoint).GetSigRL(ctx, epidGID)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: methodGetSigRL.FullName(),
	}
	handler := func(ctx context.Context, req any) (any, error) {
		return srv.(Endpoint).GetSigRL(ctx, req.(uint32))
	}
	return interceptor(ctx, epidGID, info, handler)
}

// RegisterService registers a new IAS service with the given gRPC server.
func RegisterService(server *grpc.Server, service Endpoint) {
	server.RegisterService(&serviceDesc, service)
}

// Client is a gRPC IAS endpoint client.
type Client struct {
	conn *grpc.ClientConn
}

// NewClient creates a new gRPC IAS endpoint client.
func NewClient(c *grpc.ClientConn) *Client {
	return &Client{
		conn: c,
	}
}

func (c *Client) VerifyEvidence(ctx context.Context, evidence *Evidence) (*ias.AVRBundle, error) {
	var rsp ias.AVRBundle
	if err := c.conn.Invoke(ctx, methodVerifyEvidence.FullName(), evidence, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

func (c *Client) GetSPIDInfo(ctx context.Context) (*SPIDInfo, error) {
	var rsp SPIDInfo
	if err := c.conn.Invoke(ctx, methodGetSPIDInfo.FullName(), nil, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

func (c *Client) GetSigRL(ctx context.Context, epidGID uint32) ([]byte, error) {
	var rsp []byte
	if err := c.conn.Invoke(ctx, methodGetSigRL.FullName(), epidGID, &rsp); err != nil {
		return nil, err
	}
	return rsp, nil
}

func (c *Client) Cleanup() {
}
