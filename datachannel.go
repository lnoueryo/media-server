package main

import (
	"encoding/json"

	"github.com/pion/webrtc/v4"
)

type DCEnvelope struct {
    Event   string          `json:"event"`
    Message    json.RawMessage `json:"message"`
}

func broadcastDataChannel(label string, roomId string, fromUserId string, env DCEnvelope) {
    room, ok := rooms.getRoom(roomId)
    if !ok {
        return
    }

    message, _ := json.Marshal(env)

    room.listLock.Lock()
    defer room.listLock.Unlock()

    for _, p := range room.participants {
		dc := p.DCs[label]
        if dc != nil && dc.ReadyState() == webrtc.DataChannelStateOpen {
            _ = dc.Send(message)
        }
    }

    for _, v := range room.viewers {
		dc := v.DCs[label]
        if dc != nil && dc.ReadyState() == webrtc.DataChannelStateOpen {
            _ = dc.Send(message)
        }
    }
}

func unicastDataChannel(label string, roomId string, userId string, env DCEnvelope) {
    room, ok := rooms.getRoom(roomId)
    if !ok {
        return
    }

    message, _ := json.Marshal(env)

    room.listLock.Lock()
    defer room.listLock.Unlock()

    for uid, p := range room.participants {
        if uid != userId {
            continue
        }
		dc := p.DCs[label]
        if dc != nil && dc.ReadyState() == webrtc.DataChannelStateOpen {
            _ = dc.Send(message)
        }
    }
}

func multicastDataChannel(label string, roomId string, env DCEnvelope, callback func(participant *Participant) bool) {
    room, ok := rooms.getRoom(roomId)
    if !ok {
        return
    }

    message, _ := json.Marshal(env)

    room.listLock.Lock()
    defer room.listLock.Unlock()

    for _, p := range room.participants {
        if !callback(p) {
            continue
        }
		dc := p.DCs[label]
        if dc != nil && dc.ReadyState() == webrtc.DataChannelStateOpen {
            _ = dc.Send(message)
        }
    }
}
