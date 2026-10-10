package egg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
)

// Struct envelopes keep this additive control service independent of the PTY
// protobuf contract. Existing egg socket authentication covers every method.
type RunTurnRPCServer interface {
	HandleRunTurn(context.Context, string, RunTurnRequest) (RunTurnResult, error)
}

func RegisterRunTurnRPC(server *grpc.Server, handler RunTurnRPCServer) {
	description := grpc.ServiceDesc{ServiceName: "egg.RunTurns", HandlerType: (*RunTurnRPCServer)(nil)}
	for _, method := range []string{"Reserve", "Submit", "Status", "Wait", "Result", "Stop"} {
		description.Methods = append(description.Methods, grpc.MethodDesc{MethodName: method, Handler: func(srv any, ctx context.Context, decode func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
			message := new(structpb.Struct)
			if err := decode(message); err != nil {
				return nil, err
			}
			call := func(ctx context.Context, req any) (any, error) {
				wire, err := json.Marshal(req.(*structpb.Struct).AsMap())
				if err != nil {
					return nil, status.Error(codes.InvalidArgument, "invalid run request")
				}
				var request RunTurnRequest
				decoder := json.NewDecoder(bytes.NewReader(wire))
				decoder.DisallowUnknownFields()
				if err = decoder.Decode(&request); err != nil {
					return nil, status.Error(codes.InvalidArgument, "invalid run request")
				}
				result, err := srv.(RunTurnRPCServer).HandleRunTurn(ctx, method, request)
				if err != nil {
					return nil, err
				}
				return runTurnEnvelope(result)
			}
			if interceptor == nil {
				return call(ctx, message)
			}
			return interceptor(ctx, message, &grpc.UnaryServerInfo{Server: srv, FullMethod: "/egg.RunTurns/" + method}, call)
		}})
	}
	server.RegisterService(&description, handler)
}

func runTurnEnvelope(value any) (*structpb.Struct, error) {
	wire, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var object map[string]any
	if err = json.Unmarshal(wire, &object); err != nil {
		return nil, err
	}
	return structpb.NewStruct(object)
}

func (s *Server) HandleRunTurn(ctx context.Context, method string, request RunTurnRequest) (RunTurnResult, error) {
	if s.runTurns == nil {
		return RunTurnResult{}, status.Error(codes.FailedPrecondition, "egg has no native run runtime")
	}
	var result RunTurnResult
	var err error
	switch method {
	case "Reserve":
		result, err = s.runTurns.reserve(request)
	case "Submit":
		result, err = s.runTurns.submit(request)
	case "Status":
		result, err = s.runTurns.get(request.RunID, false)
	case "Wait":
		result, err = s.runTurns.wait(ctx, request.RunID)
	case "Result":
		result, err = s.runTurns.get(request.RunID, true)
	case "Stop":
		result, err = s.runTurns.stop(request.RunID)
	default:
		return result, status.Error(codes.Unimplemented, "unknown run operation")
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return result, status.FromContextError(err).Err()
	}
	if err != nil {
		return result, status.Error(codes.FailedPrecondition, "egg run operation could not be completed: "+err.Error())
	}
	return result, nil
}

func (c *Client) runTurn(ctx context.Context, method string, request RunTurnRequest) (RunTurnResult, error) {
	var result RunTurnResult
	envelope, err := runTurnEnvelope(request)
	if err != nil {
		return result, err
	}
	response := new(structpb.Struct)
	err = c.conn.Invoke(c.authCtx(ctx), "/egg.RunTurns/"+method, envelope, response, grpc.MaxCallRecvMsgSize(int(^uint(0)>>1)))
	if err != nil {
		return result, err
	}
	wire, err := json.Marshal(response.AsMap())
	if err != nil {
		return result, err
	}
	err = json.Unmarshal(wire, &result)
	return result, err
}

func (c *Client) SubmitRunTurn(ctx context.Context, request RunTurnRequest) (RunTurnResult, error) {
	return c.runTurn(ctx, "Submit", request)
}
func (c *Client) RunTurnStatus(ctx context.Context, id string) (RunTurnResult, error) {
	return c.runTurn(ctx, "Status", RunTurnRequest{RunID: id})
}
func (c *Client) WaitRunTurn(ctx context.Context, id string) (RunTurnResult, error) {
	return c.runTurn(ctx, "Wait", RunTurnRequest{RunID: id})
}
func (c *Client) ReadRunTurnResult(ctx context.Context, id string) (RunTurnResult, error) {
	return c.runTurn(ctx, "Result", RunTurnRequest{RunID: id})
}
func (c *Client) StopRunTurn(ctx context.Context, id string) (RunTurnResult, error) {
	return c.runTurn(ctx, "Stop", RunTurnRequest{RunID: id})
}

func (c *Client) ReserveRunTurn(ctx context.Context, request RunTurnRequest) (RunTurnResult, error) {
	return c.runTurn(ctx, "Reserve", request)
}
