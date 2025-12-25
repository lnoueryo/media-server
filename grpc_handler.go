package main

import (
	"context"
	"encoding/json"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	media "streaming-media.jounetsism.biz/proto/media"
)

type MediaService struct {
	media.UnimplementedMediaServiceServer
}

func (s *MediaService) CreatePeer(
	ctx context.Context,
	req *media.CreatePeerRequest,
) (*media.Void, error) {
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
		log.Infof("DataChannel opened: %s", dc.Label())

		// テスト送信
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


	// ----- register participant -----
	spaceMember, err := GetTargetSpaceMember(roomId, user.Id); if err != nil {
		log.Errorf("space member error: %v", err)
		return nil, status.Errorf(codes.PermissionDenied, "space member not allowed: %v", err)
	}
	room.listLock.Lock()
	_, ok := room.participants[user.Id]; if ok {
		// Unicast(roomId, user.Id, "duplicate-participant", []byte{})
		unicastDataChannel("room", roomId, user.Id, DCEnvelope{"duplicate-participant", nil})
		room.listLock.Unlock()
		return nil, status.Error(codes.AlreadyExists, "duplicate participant")
	}
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
			participants := make([]UserInfo, 0)
			for _, participant := range room.participants {
				participants = append(participants, UserInfo{
					ID: participant.ID,
					Name: participant.Name,
					Email: participant.Email,
					Image: participant.Image,
				})
			}
			res, _ := json.Marshal(participants)
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
			users := make([]UserInfo, 0)
			for _, participant := range room.participants {
				users = append(users, UserInfo{
					ID: participant.ID,
					Name: participant.Name,
					Email: participant.Email,
					Image: participant.Image,
				})
			}
			if len(room.participants) == 0 {
				delete(rooms.item, roomId)
			}
			res, _ := json.Marshal(users)
			BroadcastToLobby(roomId, "access", res)
		}
	})

	// ----- track handler -----
	peerConnection.OnTrack(func(t *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		log.Infof("Got remote track: %s %s %s %s %s", t.Kind(), t.ID(), t.StreamID(), t.Msid(), t.RID())
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
	return &media.Void{}, nil
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
