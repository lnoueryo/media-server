package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/golang-jwt/jwt/v5"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func AuthGrpcInterceptor(
    ctx context.Context,
    req interface{},
    info *grpc.UnaryServerInfo,
    handler grpc.UnaryHandler,
) (interface{}, error) {

    md, ok := metadata.FromIncomingContext(ctx)
    if !ok {
        return nil, status.Error(codes.Unauthenticated, "metadata missing")
    }

    auth := md.Get("authorization")
    if len(auth) == 0 {
        return nil, status.Error(codes.Unauthenticated, "authorization missing")
    }

    token := strings.TrimPrefix(auth[0], "Bearer ")

    claims, err := verifyServiceJWT(token)
    if err != nil {
        return nil, status.Error(codes.Unauthenticated, "invalid token")
    }

    ctx = context.WithValue(ctx, "serviceClaims", claims)
    return handler(ctx, req)
}

func verifyServiceJWT(tokenString string) (*jwt.RegisteredClaims, error) {
    secret := []byte(os.Getenv("SERVICE_JWT_SECRET"))
    token, err := jwt.ParseWithClaims(
        tokenString,
        &jwt.RegisteredClaims{},
        func(token *jwt.Token) (interface{}, error) {
            return secret, nil
        },
        jwt.WithAudience("signaling-server"),
        jwt.WithIssuer("app-server"),
    )
    if err != nil {
        return nil, fmt.Errorf("parse error: %w", err)
    }

    claims, ok := token.Claims.(*jwt.RegisteredClaims)
    if !ok || !token.Valid {
        return nil, fmt.Errorf("invalid claims or token")
    }
    return claims, nil
}