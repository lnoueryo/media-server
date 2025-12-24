package main

import (
	"net"

	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"streaming-media.jounetsism.biz/proto/media"
)

func main() {
	lis, _ := net.Listen("tcp", ":50051")

	grpcServer := grpc.NewServer(grpc.UnaryInterceptor(AuthGrpcInterceptor),)

	media.RegisterMediaServiceServer(
		grpcServer,
		&MediaService{},
	)
	logrus.Info("gRPC server started on :50051")
	err := grpcServer.Serve(lis)
	if err != nil {
		logrus.Fatalf("failed to serve: %v", err)
	}

}
