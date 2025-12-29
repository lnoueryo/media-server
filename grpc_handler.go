package main

import (
	"context"
	"encoding/json"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	media "streaming-media.jounetsism.biz/proto/media"
)

type MediaService struct {
	media.UnimplementedMediaServiceServer
}

func (s *MediaService) CreatePeer(
	ctx context.Context,
	req *media.CreatePeerRequest,
) (*media.GetRoomResponse, error) {
	roomId := req.GetSpaceId()
	user := req.GetUser()
	spaceMember := req.GetSpaceMember()
	room := rooms.getOrCreate(roomId)
	_, ok := room.participants[user.Id];
	if ok {
		md := metadata.Pairs(
			"error-code", "participant-already-exists",
		)
		grpc.SetTrailer(ctx, md)
		return nil, status.Error(codes.AlreadyExists, "違うデバイスで既に参加しています")
	}
	peerConnection, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create peer connection: %v", err)
	}

	for _, typ := range []webrtc.RTPCodecType{
		webrtc.RTPCodecTypeVideo,
		webrtc.RTPCodecTypeAudio,
	} {
		if _, err := peerConnection.AddTransceiverFromKind(
			typ,
			webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly},
		); err != nil {
			return nil, status.Errorf(codes.Internal, "failed to add transceiver: %v", err)
		}
	}


	ordered := false
	maxRetransmits := uint16(0)

	options := &webrtc.DataChannelInit{
		Ordered:        &ordered,
		MaxRetransmits: &maxRetransmits,
	}
	dc, err := peerConnection.CreateDataChannel("room", options)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "create datachannel failed: %v", err)
	}
	dc.OnOpen(func() {
		log.Info("room.trackParticipants: %s", room.trackParticipants)
		jsonData, _ := json.Marshal(room.trackParticipants)
		broadcastDataChannel("room", roomId, user.Id, DCEnvelope{
			Event: "track-participant",
			Message: jsonData,
		})
		msg := []byte(`{"type":"joined","message":"welcome"}`)
		_ = dc.Send(msg)
	})

	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		if !msg.IsString {
			return
		}

		var env DCEnvelope
		if err := json.Unmarshal(msg.Data, &env); err != nil {
			log.Warnf("invalid dc message: %v", err)
			return
		}

		broadcastDataChannel("room", roomId, user.Id, env)
	})
	// payload := map[string]any{
	// 	"type": "joined",
	// 	"user": user,
	// }

	// b, _ := json.Marshal(payload)
	// dc.Send(b)
	room.listLock.Lock()
	room.participants[user.Id] = &Participant{
		spaceMember.GetId(),
		spaceMember.GetRole(),
		UserInfo{
			user.GetId(),
			user.GetEmail(),
			user.GetName(),
			user.GetImage(),
		},
		peerConnection,
		map[string]*webrtc.DataChannel{"room": dc},
	}
	room.listLock.Unlock()

	peerConnection.OnConnectionStateChange(func(p webrtc.PeerConnectionState) {
		log.Info("Connection state change: %s", p)

		switch p {
		case webrtc.PeerConnectionStateConnected:
			room, ok := rooms.getRoom(roomId);if !ok {
				return
			}
			res, _ := json.Marshal(room.GetSliceParticipants())
			BroadcastToLobby(roomId, "access", res)
		case webrtc.PeerConnectionStateFailed:
			_ = peerConnection.Close()
		case webrtc.PeerConnectionStateDisconnected:
			// 猶予を与える
			go func() {
				time.Sleep(20 * time.Second)
				if peerConnection.ConnectionState() == webrtc.PeerConnectionStateDisconnected {
					_ = peerConnection.Close()
				}
			}()
		case webrtc.PeerConnectionStateClosed:
			room, ok := rooms.getRoom(roomId);if !ok {
				return
			}
			room.listLock.Lock()
			delete(room.participants, user.Id)
			room.listLock.Unlock()
			if len(room.participants) == 0 {
				delete(rooms.item, roomId)
			}
			res, _ := json.Marshal(room.GetSliceParticipants())
			BroadcastToLobby(roomId, "access", res)
		}
	})

	// ----- track handler -----
	peerConnection.OnTrack(func(t *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		log.Infof("Got remote track: %s %s %s %s %s", t.Kind(), t.ID(), t.StreamID(), t.Msid(), t.RID())
		room, ok := rooms.getRoom(roomId)
		if !ok {
			return
		}
		trackLocal := addTrack(room, t)
		if trackLocal == nil {
			return
		}
		track := &TrackParticipant{
			UserInfo{
				user.GetId(),
				user.GetEmail(),
				user.GetName(),
				user.GetImage(),
			},
			t.StreamID(),
			t.ID(),
		}
		room.trackParticipants[t.StreamID()] = track
		jsonData, _ := json.Marshal(room.trackParticipants)
		defer func() {
			removeTrack(room, trackLocal)
			delete(room.trackParticipants, t.StreamID())
		}()
		// BroadcastToRoom(roomId, "track-participant", jsonData)
		broadcastDataChannel("room", roomId, user.Id, DCEnvelope{
			Event: "track-participant",
			Message: jsonData,
		})

		buf := make([]byte, 1500)
		pkt := &rtp.Packet{}

		for {
			n, _, err := t.Read(buf)
			if err != nil {
				return
			}

			if pkt.Unmarshal(buf[:n]) != nil {
				continue
			}

			pkt.Extension = false
			pkt.Extensions = nil
			trackLocal.WriteRTP(pkt)
		}
	})

	signalPeerConnections(room)

	return &media.GetRoomResponse{
		Id:           room.ID,
		Participants: room.GetSliceParticipants(),
	}, nil
}

func (s *MediaService) CreateViewerPeer(
	ctx context.Context,
	req *media.CreateViewerPeerRequest,
) (*media.GetRoomResponse, error) {
	roomId := req.GetSpaceId()
	user := req.GetUser()
	room := rooms.getOrCreate(roomId)
	peerConnection, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create peer connection: %v", err)
	}

	for _, typ := range []webrtc.RTPCodecType{
		webrtc.RTPCodecTypeVideo,
		webrtc.RTPCodecTypeAudio,
	} {
		if _, err := peerConnection.AddTransceiverFromKind(
			typ,
			webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly},
		); err != nil {
			return nil, status.Errorf(codes.Internal, "failed to add transceiver: %v", err)
		}
	}


	ordered := false
	maxRetransmits := uint16(0)

	options := &webrtc.DataChannelInit{
		Ordered:        &ordered,
		MaxRetransmits: &maxRetransmits,
	}
	dc, err := peerConnection.CreateDataChannel("room", options)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "create datachannel failed: %v", err)
	}
	dc.OnOpen(func() {
		jsonData, _ := json.Marshal(room.trackParticipants)
		broadcastDataChannel("room", roomId, user.Id, DCEnvelope{
			Event: "track-participant",
			Message: jsonData,
		})
	})

	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		if !msg.IsString {
			return
		}

		var env DCEnvelope
		if err := json.Unmarshal(msg.Data, &env); err != nil {
			log.Warnf("invalid dc message: %v", err)
			return
		}

		broadcastDataChannel("room", roomId, user.Id, env)
	})

	room.listLock.Lock()
	room.viewers[user.Id] = &Viewer{
		UserInfo{
			user.GetId(),
			user.GetEmail(),
			user.GetName(),
			user.GetImage(),
		},
		peerConnection,
		map[string]*webrtc.DataChannel{"room": dc},
	}
	room.listLock.Unlock()

	peerConnection.OnConnectionStateChange(func(p webrtc.PeerConnectionState) {
		switch p {
		case webrtc.PeerConnectionStateFailed:
			_ = peerConnection.Close()
		case webrtc.PeerConnectionStateDisconnected:
			go func() {
				time.Sleep(20 * time.Second)
				if peerConnection.ConnectionState() == webrtc.PeerConnectionStateDisconnected {
					_ = peerConnection.Close()
				}
			}()
		case webrtc.PeerConnectionStateClosed:
			room, ok := rooms.getRoom(roomId);if !ok {
				return
			}
			room.listLock.Lock()
			delete(room.viewers, user.Id)
			room.listLock.Unlock()
		}
	})

	signalPeerConnections(room)

	return &media.GetRoomResponse{
		Id:           room.ID,
		Participants: room.GetSliceParticipants(),
	}, nil
}

func (s *MediaService) AddCandidate(
	ctx context.Context,
	req *media.AddCandidateRequest,
) (*media.Void, error) {
	roomId := req.SpaceId
	user := req.User
	candidate := req.Candidate
	room, _ := rooms.getRoom(roomId)
	participant, ok := room.participants[user.Id]; if !ok {
		return nil, status.Error(codes.NotFound, "participant not found")
	}
	var cand webrtc.ICECandidateInit
	json.Unmarshal([]byte(candidate), &cand)
	if err := participant.PC.AddICECandidate(cand); err != nil {
		log.Errorf("ice add error: %v", err)
	}
	return &media.Void{}, nil
}

func (s *MediaService) AddViewerCandidate(
	ctx context.Context,
	req *media.AddCandidateRequest,
) (*media.Void, error) {
	roomId := req.SpaceId
	user := req.User
	candidate := req.Candidate
	room, ok := rooms.getRoom(roomId)
	if !ok {
		return nil, status.Error(codes.NotFound, "room not found")
	}
	viewer, ok := room.viewers[user.Id]; if !ok {
		return nil, status.Error(codes.NotFound, "participant not found")
	}
	var cand webrtc.ICECandidateInit
	json.Unmarshal([]byte(candidate), &cand)
	if err := viewer.PC.AddICECandidate(cand); err != nil {
		log.Errorf("ice add error: %v", err)
	}
	return &media.Void{}, nil
}

func (s *MediaService) SetAnswer(
	ctx context.Context,
	req *media.SetAnswerRequest,
) (*media.Void, error) {
	roomId := req.SpaceId
	user := req.User
	answer := req.Answer
	room, _ := rooms.getRoom(roomId)
	participant, ok := room.participants[user.Id]; if !ok {
		return nil, status.Error(codes.NotFound, "participant not found")
	}
	var ans webrtc.SessionDescription
	json.Unmarshal([]byte(answer), &ans)
	participant.PC.SetRemoteDescription(ans)
	return &media.Void{}, nil
}

func (s *MediaService) SetViewerAnswer(
	ctx context.Context,
	req *media.SetAnswerRequest,
) (*media.Void, error) {
	roomId := req.SpaceId
	user := req.User
	answer := req.Answer
	room, ok := rooms.getRoom(roomId)
	if !ok {
		return nil, status.Error(codes.NotFound, "room not found")
	}
	viewer, ok := room.viewers[user.Id]; if !ok {
		return nil, status.Error(codes.NotFound, "participant not found")
	}
	var ans webrtc.SessionDescription
	json.Unmarshal([]byte(answer), &ans)
	viewer.PC.SetRemoteDescription(ans)
	return &media.Void{}, nil
}

func (s *MediaService) GetRoom(
	ctx context.Context,
	req *media.GetRoomRequest,
) (*media.GetRoomResponse, error) {
	room, ok := rooms.getRoom(req.SpaceId)
	log.Info("GetRoom gRPC called: spaceId=%s, found=%v", req.SpaceId, ok)
	if !ok {
		md := metadata.Pairs(
			"error-code", "room-not-found",
		)
		grpc.SetTrailer(ctx, md)
		return nil, status.Error(codes.NotFound, "roomが存在しません")
	}

	return &media.GetRoomResponse{
		Id:           room.ID,
		Participants: room.GetSliceParticipants(),
	}, nil
}

func (s *MediaService) RemoveParticipant(
	ctx context.Context,
	req *media.RemoveParticipantRequest,
) (*media.GetRoomResponse, error) {
	roomId := req.GetSpaceId()
	userId := req.GetUserId()

	// ルームが存在しない場合
	room, ok := rooms.getRoom(roomId)
	if !ok {
		return nil, status.Error(codes.NotFound, "no-target-room")
	}

	// 参加者が存在しない場合
	_, ok = room.participants[userId]
	if !ok {
		return nil, status.Error(codes.NotFound, "no-target-user")
	}
	unicastDataChannel("room", roomId, userId, DCEnvelope{"close", nil})
	room.listLock.Lock()
	// 切断処理
	delete(room.participants, userId)
	// WebRTC のクローズ
	room.listLock.Unlock()
	// 残った参加者を整形

	return &media.GetRoomResponse{
		Id:           room.ID,
		Participants: room.GetSliceParticipants(),
	}, nil
}

func (s *MediaService) RequestEntry(
	ctx context.Context,
	req *media.SpaceMemberRequest,
) (*media.Void, error) {
	_, ok := rooms.getRoom(req.SpaceId)
	if !ok {
		md := metadata.Pairs(
			"error-code", "room-not-found",
		)
		grpc.SetTrailer(ctx, md)
		return nil, status.Error(codes.NotFound, "roomが存在しません")
	}

	jsonData, _ := json.Marshal(req.SpaceMember)
	multicastDataChannel("room", req.SpaceId, DCEnvelope{
		Event: "participant-request",
		Message: jsonData,
	}, func(participant *Participant) bool {
		return participant.Role == "owner"
	})

	return &media.Void{}, nil
}

func (s *MediaService) ChangeMemberState(
	ctx context.Context,
	req *media.SpaceMemberRequest,
) (*media.Void, error) {
	_, ok := rooms.getRoom(req.SpaceId)
	if !ok {
		md := metadata.Pairs(
			"error-code", "room-not-found",
		)
		grpc.SetTrailer(ctx, md)
		return nil, status.Error(codes.NotFound, "roomが存在しません")
	}

	jsonData, _ := json.Marshal(req.SpaceMember)
	multicastDataChannel("room", req.SpaceId, DCEnvelope{
		Event: "change-member-state",
		Message: jsonData,
	}, func(participant *Participant) bool {
		return participant.Role == "owner"
	})

	return &media.Void{}, nil
}
