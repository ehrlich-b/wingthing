package egg

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/ehrlich-b/wingthing/internal/permissions"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
)

type permissionRPCServer interface {
	permissionOperation(context.Context, string, permissions.Response) (map[string]any, error)
}

func registerPermissionRPC(server *grpc.Server, handler permissionRPCServer) {
	desc := grpc.ServiceDesc{ServiceName: "egg.Permissions", HandlerType: (*permissionRPCServer)(nil)}
	for _, method := range []string{"List", "Check", "Respond"} {
		desc.Methods = append(desc.Methods, grpc.MethodDesc{MethodName: method, Handler: func(srv any, ctx context.Context, decode func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
			m := new(structpb.Struct)
			if err := decode(m); err != nil {
				return nil, err
			}
			call := func(ctx context.Context, req any) (any, error) {
				wire, _ := json.Marshal(req.(*structpb.Struct).AsMap())
				var r permissions.Response
				d := json.NewDecoder(bytes.NewReader(wire))
				d.DisallowUnknownFields()
				if d.Decode(&r) != nil {
					return nil, status.Error(codes.InvalidArgument, "invalid permission binding")
				}
				if method == "List" && r != (permissions.Response{}) {
					return nil, status.Error(codes.InvalidArgument, "list takes no decision")
				}
				result, err := srv.(permissionRPCServer).permissionOperation(ctx, method, r)
				if err != nil {
					return nil, status.Error(codes.FailedPrecondition, err.Error())
				}
				return runTurnEnvelope(result)
			}
			if interceptor == nil {
				return call(ctx, m)
			}
			return interceptor(ctx, m, &grpc.UnaryServerInfo{Server: srv, FullMethod: "/egg.Permissions/" + method}, call)
		}})
	}
	server.RegisterService(&desc, handler)
}
func (c *Client) PermissionCall(ctx context.Context, method string, r permissions.Response) (map[string]any, error) {
	envelope, err := runTurnEnvelope(r)
	if err != nil {
		return nil, err
	}
	if method == "List" {
		envelope, _ = structpb.NewStruct(map[string]any{})
	}
	result := new(structpb.Struct)
	if err = c.conn.Invoke(c.authCtx(ctx), "/egg.Permissions/"+method, envelope, result); err != nil {
		return nil, err
	}
	return result.AsMap(), nil
}
