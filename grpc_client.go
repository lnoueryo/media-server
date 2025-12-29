package main

import (
	"context"
	"log"
	"os"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	application "streaming-media.jounetsism.biz/proto/application"
	signaling "streaming-media.jounetsism.biz/proto/signaling"
)
var (
    spaceServiceClient application.SpaceServiceClient
    signalingServiceClient signaling.SignalingServiceClient
    grpcConn           *grpc.ClientConn
    onceInitSpace           sync.Once
    onceInitRoom           sync.Once
)

func initSpaceServiceClient() {
    conn, err := grpc.NewClient(
        applicationServerOrigin,
        grpc.WithTransportCredentials(insecure.NewCredentials()),
    )
    if err != nil {
        log.Fatalf("failed to init gRPC client: %v", err)
    }
    grpcConn = conn
    spaceServiceClient = application.NewSpaceServiceClient(conn)
}

func initRoomClient() {
    conn, err := grpc.Dial(
        signalingServerOrigin,
        grpc.WithTransportCredentials(insecure.NewCredentials()),
    )
    if err != nil {
        log.Fatalf("failed to dial signaling server: %v", err)
    }
    signalingServiceClient = signaling.NewSignalingServiceClient(conn)
}

func GetTargetSpaceMember(roomId string, userId string) (*application.GetTargetSpaceMemberResponse, error) {
    onceInitSpace.Do(initSpaceServiceClient)
    token := createServiceJWT()
    md := metadata.New(map[string]string{
        "authorization": "Bearer " + token,
    })
    ctx := metadata.NewOutgoingContext(context.Background(), md)
    ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
    defer cancel()

    resp, err := spaceServiceClient.GetTargetSpaceMember(ctx, &application.GetTargetSpaceMemberRequest{
        SpaceId: roomId,
        UserId:  userId,
    })
    if err != nil {
        return nil, err
    }
    return resp, nil
}

func BroadcastToLobby(spaceId string, event string, data []byte) error {
    onceInitRoom.Do(initRoomClient)
    token := createServiceJWT()
    md := metadata.New(map[string]string{
        "authorization": "Bearer " + token,
    })
    ctx := metadata.NewOutgoingContext(context.Background(), md)
    ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
    defer cancel()

    _, err := signalingServiceClient.BroadcastToLobby(ctx, &signaling.BroadcastRequest{
        SpaceId: spaceId,
        Event:   event,
        Data:    data,
    })
    if err != nil {
        return err
    }

    return nil
}

func Unicast(spaceId string, userId string, event string, data []byte) error {
    onceInitRoom.Do(initRoomClient)
    token := createServiceJWT()
    md := metadata.New(map[string]string{
        "authorization": "Bearer " + token,
    })
    ctx := metadata.NewOutgoingContext(context.Background(), md)
    ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
    defer cancel()

    _, err := signalingServiceClient.Unicast(ctx, &signaling.UnicastRequest{
        SpaceId: spaceId,
        UserId:  userId,
        Event:   event,
        Data:    data,
    })
    if err != nil {
        return err
    }

    return nil
}

func createServiceJWT() string {
	secret := []byte(os.Getenv("SERVICE_JWT_SECRET"))

	claims := jwt.RegisteredClaims{
		Issuer:    "app-server",
		Audience:  []string{"signaling-server"},
		Subject:   "media-service", // 任意（識別用）
		IssuedAt:  jwt.NewNumericDate(time.Now()),
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(1 * time.Minute)),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)

	signed, err := token.SignedString(secret)
	if err != nil {
		panic(err)
	}

	return signed
}