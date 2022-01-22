package auth

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/palantir/stacktrace"
	"github.com/tnyim/jungletv/utils/wrappedstream"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// UserAuthorizer allows for checking if users are authorized to use the service
type UserAuthorizer interface {
	IsRemoteAddressAllowed(remoteAddr string) bool
	IsRewardAddressAllowed(rewardAddr string) bool
}

// Interceptor intercepts gRPC requests to ensure user authentication and authorization
type Interceptor struct {
	jwtManager                  *JWTManager
	minPermissionLevelForMethod map[string]PermissionLevel
	authorizer                  UserAuthorizer
}

// NewInterceptor returns a new Interceptor
func NewInterceptor(jwtManager *JWTManager, authorizer UserAuthorizer) *Interceptor {
	return &Interceptor{
		jwtManager,
		make(map[string]PermissionLevel),
		authorizer,
	}
}

// SetMinimumPermissionLevelForMethod sets the minimum permission level required to use the given method
func (interceptor *Interceptor) SetMinimumPermissionLevelForMethod(method string, level PermissionLevel) {
	interceptor.minPermissionLevelForMethod[method] = level
}

// Unary intercepts unary RPC requests
func (interceptor *Interceptor) Unary() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req interface{},
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (interface{}, error) {
		newCtx, err := interceptor.authorize(ctx, info.FullMethod)
		if err != nil {
			return nil, err
		}
		return handler(newCtx, req)
	}
}

// Stream intercepts stream RPC requests
func (interceptor *Interceptor) Stream() grpc.StreamServerInterceptor {
	return func(
		srv interface{},
		stream grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		newCtx, err := interceptor.authorize(stream.Context(), info.FullMethod)
		if err != nil {
			return err
		}
		wrapped := wrappedstream.WrapServerStream(stream)
		wrapped.WrappedContext = newCtx
		return handler(srv, wrapped)
	}
}

func (interceptor *Interceptor) authorize(ctx context.Context, method string) (context.Context, error) {
	authErr := status.Errorf(codes.Unauthenticated, "metadata is not provided")
	var claims *UserClaims
	md, ok := metadata.FromIncomingContext(ctx)
	remoteAddress := ""
	if ok {
		remoteAddress = interceptor.getRemoteAddress(ctx, md)
		if !interceptor.authorizer.IsRemoteAddressAllowed(remoteAddress) {
			return ctx, status.Error(codes.PermissionDenied, "no permission to access this RPC")
		}

		claims, authErr = interceptor.tryAuthenticate(ctx, md)
	}
	ctx = context.WithValue(ctx, remoteAddressContextKey{}, remoteAddress)

	ipCountry := "XX"
	if len(md["cf-ipcountry"]) > 0 {
		ipCountry = md["cf-ipcountry"][0]
	}
	ctx = context.WithValue(ctx, ipCountryRequestKey{}, ipCountry)

	if authErr == nil {
		if !interceptor.authorizer.IsRewardAddressAllowed(claims.RewardAddress) {
			return ctx, status.Error(codes.PermissionDenied, "no permission to access this RPC")
		}

		// place claims in context
		ctx = context.WithValue(ctx, userClaimsContextKey{}, claims)

		if time.Until(time.Unix(claims.ExpiresAt, 0)) < interceptor.jwtManager.tokenLifetimes[claims.PermLevel]/2 {
			err := interceptor.renewAuthToken(ctx, claims)
			if err != nil {
				return ctx, stacktrace.Propagate(err, "")
			}
		}
	}

	minPermissionLevel := interceptor.minPermissionLevelForMethod[method]
	if PermissionLevelOrder[minPermissionLevel] <= PermissionLevelOrder[UnauthenticatedPermissionLevel] {
		// maybe authErr != nil, but we don't care because this method doesn't require auth
		// (we have already placed the claims in the context so the request handler can optionally see
		// who the user is, even if authentication is not required)
		return ctx, nil
	}

	// permission levels beyond UnauthenticatedPermissionLevel require successful auth
	if authErr != nil {
		return ctx, authErr
	}

	if PermissionLevelOrder[minPermissionLevel] > PermissionLevelOrder[claims.PermLevel] {
		return ctx, status.Errorf(codes.PermissionDenied, "no permission to access this RPC")
	}

	return ctx, nil
}

func (interceptor *Interceptor) tryAuthenticate(ctx context.Context, md metadata.MD) (*UserClaims, error) {
	values := md["authorization"]
	if len(values) == 0 {
		return nil, status.Errorf(codes.Unauthenticated, "authorization token is not provided")
	}

	accessToken := values[0]
	claims, err := interceptor.jwtManager.Verify(accessToken)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "access token is invalid: %v", err)
	}
	return claims, nil
}

func (interceptor *Interceptor) renewAuthToken(ctx context.Context, claims *UserClaims) error {
	token, expiration, err := interceptor.jwtManager.Generate(claims.RewardAddress, claims.PermLevel, claims.Username)
	if err != nil {
		return stacktrace.Propagate(err, "")
	}

	err = grpc.SetHeader(ctx, metadata.New(map[string]string{
		"X-Replacement-Authorization-Token":      token,
		"X-Replacement-Authorization-Expiration": expiration.Format("2006-01-02T15:04:05.999Z07:00"),
	}))
	return stacktrace.Propagate(err, "")
}

func (interceptor *Interceptor) getRemoteAddress(ctx context.Context, md metadata.MD) string {
	ip := ""

	getMetadataIP := func(header string) string {
		value := strings.Join(md[header], ",")
		return strings.TrimSpace(strings.Split(value, ",")[0])
	}

	for _, header := range []string{"x-real-ip", "real-ip", "x-forwarded-for", "x-forwarded", "forwarded-for", "forwarded"} {
		ip = getMetadataIP(header)
		if ip != "" {
			break
		}
	}

	if ip == "" {
		if p, ok := peer.FromContext(ctx); ok {
			ip = p.Addr.String()
		} else {
			return ""
		}
	}

	netIP, _, _, err := parseIPPort(ip)
	if err != nil {
		return ip
	}
	return netIP.String()
}

func parseIPPort(s string) (ip net.IP, port, space string, err error) {
	ip = net.ParseIP(s)
	if ip == nil {
		var host string
		host, port, err = net.SplitHostPort(s)
		if err != nil {
			return
		}
		if port != "" {
			// This check only makes sense if service names are not allowed
			if _, err = strconv.ParseUint(port, 10, 16); err != nil {
				return
			}
		}
		ip = net.ParseIP(host)
	}
	if ip == nil {
		err = errors.New("invalid address format")
	} else {
		space = "IPv6"
		if ip4 := ip.To4(); ip4 != nil {
			space = "IPv4"
			ip = ip4
		}
	}
	return
}
