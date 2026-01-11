package main

// import (
// 	"bytes"
// 	"context"
// 	"fmt"
// 	"io"

// 	// "log"
// 	"math/rand"
// 	"net"
// 	"os"
// 	"os/exec"
// 	"path/filepath"
// 	"text/template"
// 	"time"

// 	"github.com/google/uuid"
// 	"github.com/pion/rtp"
// 	"github.com/pion/webrtc/v4"
// )

// type Recorder struct {
// 	cmd        *exec.Cmd
// 	recordChannels map[string]chan []byte
// 	startRtpTS map[string]uint32
// 	stopAtRtpTS map[string]uint32
// }

// func NewRecorder() *Recorder {
// 	return &Recorder{
// 		recordChannels: make(map[string]chan []byte),
// 		startRtpTS:  make(map[string]uint32),
// 		stopAtRtpTS: make(map[string]uint32),
// 	}
// }

// //
// // -------------- START (video + audio 同時) --------------
// //
// func (r *Recorder) Start(remoteTracks *RemoteTracks) {

// 	ctx, cancel := context.WithCancel(context.Background())

// 	// FFmpeg 起動（video + audio 用 SDP）
// 	portVideo, portAudio, cmd, _, err := RunLocalFFmpeg(remoteTracks.video, remoteTracks.audio)
// 	if err != nil {
// 		fmt.Println("FFmpeg 起動エラー:", err)
// 		return
// 	}
// 	r.cmd = cmd

// 	fmt.Println("FFmpeg started. videoPort =", portVideo, "audioPort =", portAudio)

// 	// 🎥 映像
// 	if remoteTracks.video != nil {
// 		go SendRTPToLocalFFmpeg(ctx, cancel, remoteTracks.video, portVideo, r)
// 	}
// 	// 🔊 音声
// 	if remoteTracks.audio != nil {
// 		go SendRTPToLocalFFmpeg(ctx, cancel, remoteTracks.audio, portAudio, r)
// 	}
// }

// //
// // -------------- STOP (video/audio の両方で終了) --------------
// //
// func (r *Recorder) Stop(TrackID string, stopClientTimestampMs uint64) {

// 	// clientTimestamp(ms) → RTP timestamp(90kHz)
// 	stopRtp := uint32(stopClientTimestampMs * 90)

// 	// 映像トラックの最初の RTP TS を基準にする
// 	r.stopAtRtpTS[TrackID] = r.startRtpTS[TrackID] + stopRtp

// 	fmt.Printf("🟡 StopAtRtpTS = %d (start=%d + %d)\n",
// 		r.stopAtRtpTS[TrackID], r.startRtpTS[TrackID], stopRtp)

// 	// ⚠ ここでは cancel() を呼ばない
// 	// SendRTP の内部で stopAtRtpTS になったら自動で止まる
// }

// const sdpTemplate = `v=0
// o=- 0 0 IN IP4 127.0.0.1
// s=WebRTC Stream
// c=IN IP4 127.0.0.1
// t=0 0

// m=video {{.VideoPort}} RTP/AVP 96
// a=rtpmap:96 {{.VideoCodec}}/90000

// m=audio {{.AudioPort}} RTP/AVP 111
// a=rtpmap:111 opus/48000/2
// `

// type sdpInfo struct {
// 	Codec string
// 	Port  int
// }

// func normalizeCodec(codec string) string {
// 	if codec == "video/VP8" {
// 		return "VP8"
// 	}
// 	if codec == "video/H264" {
// 		return "H264"
// 	}
// 	return "VP8"
// }

// func RunLocalFFmpeg(video *webrtc.TrackRemote, audio *webrtc.TrackRemote) (int, int, *exec.Cmd, io.WriteCloser, error) {

//     rand.Seed(time.Now().UnixNano())
//     videoPort := 50000 + rand.Intn(5000)
//     audioPort := 55000 + rand.Intn(5000)

//     videoCodec := normalizeCodec(video.Codec().MimeType)

//     out := filepath.Join("output", uuid.New().String()+".webm")
//     os.MkdirAll("output", 0755)

//     // ★あなたが指定したコマンドをそのまま使う
//     cmd := exec.Command(
//         "ffmpeg",
// 		// "-loglevel", "debug",
//         "-protocol_whitelist", "file,pipe,udp,rtp,fd",
//         "-i", "-",       // SDP 入力
//         "-c:v", "libvpx",
//         "-c:a", "copy",
// 		"-analyzeduration", "100M",
// 		"-probesize", "100M",
// 		"-thread_queue_size", "4096",
//         out,
//     )

//     cmd.Stdout = os.Stdout
//     cmd.Stderr = os.Stderr

//     stdin, err := cmd.StdinPipe()
//     if err != nil {
//         return 0, 0, nil, nil, err
//     }

//     if err := cmd.Start(); err != nil {
//         return 0, 0, nil, nil, err
//     }

//     // ★ 2ポート用 SDP を書く
//     sdpData := struct {
//         VideoPort  int
//         AudioPort  int
//         VideoCodec string
//     }{
//         VideoPort:  videoPort,
//         AudioPort:  audioPort,
//         VideoCodec: videoCodec,
//     }

//     var buf bytes.Buffer
//     tmpl := template.Must(template.New("sdp").Parse(sdpTemplate))
//     tmpl.Execute(&buf, sdpData)

//     stdin.Write(buf.Bytes())
//     stdin.Close()

//     return videoPort, audioPort, cmd, stdin, nil
// }

// func SendRTPToLocalFFmpeg(ctx context.Context, cancel context.CancelFunc, t *webrtc.TrackRemote, port int, r *Recorder) {
// 	r.startRtpTS[t.ID()] = 0
// 	r.stopAtRtpTS[t.ID()] = 0
// 	r.recordChannels[t.ID()] = make(chan []byte, 8192)
// 	addr := fmt.Sprintf("127.0.0.1:%d", port)
// 	conn, _ := net.Dial("udp", addr)
// 	// writer := bufio.NewWriterSize(conn, 65536)
// 	defer func() {
// 		conn.Close()
// 		close(r.recordChannels[t.ID()])
// 		delete(r.recordChannels, t.ID())
// 	}()

// 	for {
// 		select {
// 		case <-ctx.Done():
// 			fmt.Println("🛑 RTP sending stopped (context canceled)")
// 			return
// 		case pktBytes := <-r.recordChannels[t.ID()]:
// 			// ffmpeg に送るだけ
// 			var pkt rtp.Packet
// 			if err := pkt.Unmarshal(pktBytes); err == nil {

// 				// ★ 最初のパケットの timestamp を基準値にする
// 				if r.startRtpTS[t.ID()] == 0 {
// 					r.startRtpTS[t.ID()] = pkt.Timestamp
// 					fmt.Println("▶ Set startRtpTS =", r.startRtpTS[t.ID()])
// 				}

// 				rtpTS := pkt.Timestamp

// 				if r.stopAtRtpTS[t.ID()] > 0 {
// 					// fmt.Printf("rtpTS=%d  stopAt=%d\n", rtpTS, r.stopAtRtpTS[t.ID()])
// 				}

// 				// ---- ★ 停止ポイント到達 ----
// 				if r.stopAtRtpTS[t.ID()] > 0 && rtpTS >= r.stopAtRtpTS[t.ID()] {
// 					fmt.Printf("🟥 Stop point reached. rtpTS=%d >= %d\n",
// 						rtpTS, r.stopAtRtpTS[t.ID()])
// 					cancel()
// 					return
// 				}
// 			}
// 			// log.Printf("Send packet to FFmpeg: trackID=%s, pktLen=%d", t.ID(), len(pktBytes))
// 			go conn.Write(pktBytes)
// 		// default:
// 		// 	logrus.Infof("record channel full (ID=%s) sz=%d", t.ID(), len(r.recordChannels[t.ID()]))
// 		}
// 	}
// }