// ==============================
// rooms.go
// ==============================

package main

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
)

var rooms = &Rooms{
	map[string]*Room{},
	sync.RWMutex{},
}

type UserInfo struct {
    ID    string `json:"id"`
    Email string `json:"email"`
    Name  string `json:"name"`
    Image  string `json:"image"`
}

type Participant struct {
    MemberID string `json:"memberId"`
    Role string `json:"role"`
	UserInfo
	PC *webrtc.PeerConnection
}

type TrackParticipant struct{
    UserInfo
    StreamID string `json:"streamId"`
    TrackID  string `json:"trackId"`
}

type Room struct {
	ID           string
	listLock     sync.Mutex
	participants  map[string]*Participant
	trackLocals   map[string]*webrtc.TrackLocalStaticRTP
	trackRemotes  map[string]*webrtc.TrackRemote
    trackParticipants map[string]*TrackParticipant
	cancelFunc    context.CancelFunc
}

func (r *Room) GetSliceParticipants() []*UserInfo {
    participants := make([]*UserInfo, 0)
    for _, participant := range r.participants {
        participants = append(participants, &participant.UserInfo)
    }
    return participants
}

type Rooms struct {
	item map[string]*Room
	lock sync.RWMutex
}

func (r *Rooms) getRoom(id string) (*Room, bool) {
	r.lock.RLock()
	defer r.lock.RUnlock()
	room, ok := r.item[id]
	return room, ok
}

func (r *Rooms) getOrCreate(id string) *Room {
    r.lock.Lock()
    defer r.lock.Unlock()

    room := r.item[id]
    if room != nil {
        return room
    }

    room = &Room{
        ID:            id,
        participants:  make(map[string]*Participant),
        trackLocals:   make(map[string]*webrtc.TrackLocalStaticRTP),
        trackRemotes:  make(map[string]*webrtc.TrackRemote),
        trackParticipants: make(map[string]*TrackParticipant),
    }
    r.item[id] = room

    // --- Keyframe Ticker（旧 main の処理を Room に移動） ---
    ctx, cancel := context.WithCancel(context.Background())
    room.cancelFunc = cancel

    go func(room *Room) {
        ticker := time.NewTicker(1 * time.Second)
        defer ticker.Stop()

        for {
            select {
            case <-ctx.Done():
                return
            case <-ticker.C:
                dispatchKeyFrame(room)
            }
        }
    }(room)

    return room
}

func (r *Rooms) deleteRoom(id string) {
	r.lock.Lock()
	defer r.lock.Unlock()
	delete(r.item, id)
}

// --------------------------------------------------------
// Track handling
// --------------------------------------------------------

func addTrack(room *Room, t *webrtc.TrackRemote) *webrtc.TrackLocalStaticRTP {
    room.listLock.Lock()
    defer func() {
        room.listLock.Unlock()
        signalPeerConnections(room)
    }()

    trackLocal, err := webrtc.NewTrackLocalStaticRTP(
        t.Codec().RTPCodecCapability, t.ID(), t.StreamID(),
    )
    if err != nil {
        panic(err)
    }

    room.trackLocals[t.ID()] = trackLocal
    room.trackRemotes[t.ID()] = t

    return trackLocal
}

func removeTrack(room *Room, t *webrtc.TrackLocalStaticRTP) {
    room.listLock.Lock()
    defer func() {
        room.listLock.Unlock()
        signalPeerConnections(room)
    }()
    delete(room.trackLocals, t.ID())
    delete(room.trackRemotes, t.ID())
}

// --------------------------------------------------------
// Offer 再生成
// --------------------------------------------------------

func signalPeerConnections(room *Room) {
    room.listLock.Lock()
    defer func() {
        room.listLock.Unlock()
        dispatchKeyFrame(room)
        rooms.cleanupEmptyRoom(room.ID)
    }()

    attemptSync := func() bool {
        for id, p := range room.participants {
            pc := p.PC

            if pc.ConnectionState() == webrtc.PeerConnectionStateClosed {
                delete(room.participants, id)
                return true
            }

            existing := map[string]bool{}

            for _, sender := range pc.GetSenders() {
                if sender.Track() != nil {
                    tid := sender.Track().ID()
                    existing[tid] = true

                    if _, ok := room.trackLocals[tid]; !ok {
                        if err := pc.RemoveTrack(sender); err != nil {
                            return true
                        }
                    }
                }
            }

            for _, receiver := range pc.GetReceivers() {
                if receiver.Track() != nil {
                    existing[receiver.Track().ID()] = true
                }
            }

            for tid, tl := range room.trackLocals {
                if !existing[tid] {
                    if _, err := pc.AddTrack(tl); err != nil {
                        return true
                    }
                }
            }

            offer, err := pc.CreateOffer(nil)
            if err != nil {
                return true
            }
            if err := pc.SetLocalDescription(offer); err != nil {
                return true
            }
            // gRPCで送信
            offerJSON, _ := json.Marshal(offer)
            // p.WS.WriteJSON(&WebsocketMessage{
            //     Event: "offer",
            //     Data:  string(offerJSON),
            // })
            Unicast(room.ID, p.ID, "offer", offerJSON)
        }
        return false
    }

    for i := 0; ; i++ {
        if i == 25 {
            go func() {
                time.Sleep(3 * time.Second)
                signalPeerConnections(room)
            }()
            return
        }
        if !attemptSync() {
            break
        }
    }
}

func (r *Rooms) cleanupEmptyRoom(id string) {
	room, ok := r.getRoom(id)
	if !ok {
		return
	}

	room.listLock.Lock()
	defer room.listLock.Unlock()

	if len(room.participants) == 0 &&
		len(room.trackLocals) == 0 &&
		len(room.trackRemotes) == 0 &&
		len(room.trackParticipants) == 0 {

		room.cancelFunc()
		r.deleteRoom(id)
	}
}

// --------------------------------------------------------
// KeyFrame
// --------------------------------------------------------

func dispatchKeyFrame(room *Room) {
    room.listLock.Lock()
    defer room.listLock.Unlock()

    for _, p := range room.participants {
        pc := p.PC
        for _, r := range pc.GetReceivers() {
            if r.Track() != nil {
                pc.WriteRTCP([]rtcp.Packet{
                    &rtcp.PictureLossIndication{
                        MediaSSRC: uint32(r.Track().SSRC()),
                    },
                })
            }
        }
    }
}