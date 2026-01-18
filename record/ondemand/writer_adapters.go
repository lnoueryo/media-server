package ondemand

import (
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4/pkg/media/ivfwriter"
	"github.com/pion/webrtc/v4/pkg/media/oggwriter"
)

type RTPWriter interface {
	WriteRTP(pkt *rtp.Packet) error
	Close() error
}

func NewIVFWriter(path string, codec string) RTPWriter {
	w, _ := ivfwriter.New(path, ivfwriter.WithCodec("video/VP8"))
	return &ivfAdapter{w}
}

func NewOggWriter(path string) RTPWriter {
	w, _ := oggwriter.New(path, 48000, 2)
	return &oggAdapter{w}
}

type ivfAdapter struct {
	*ivfwriter.IVFWriter
}

func (a *ivfAdapter) WriteRTP(pkt *rtp.Packet) error {
	a.IVFWriter.WriteRTP(pkt)
	return nil
}

func (a *ivfAdapter) Close() error {
	return a.IVFWriter.Close()
}

type oggAdapter struct {
	*oggwriter.OggWriter
}

func (a *oggAdapter) WriteRTP(pkt *rtp.Packet) error {
	a.OggWriter.WriteRTP(pkt)
	return nil
}

func (a *oggAdapter) Close() error {
	return a.OggWriter.Close()
}