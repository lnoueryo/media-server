package ondemand

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"streaming-media.jounetsism.biz/record"
)

type Recorder struct {
	roomId  string
	mu sync.Mutex
	packetChannels map[string]chan *rtp.Packet
	stop     chan struct{}
	wg       sync.WaitGroup
	cancelFuncs map[string]context.CancelFunc
	outputDir string
	startTime time.Time
	endTime time.Time
	// userId -> trackId -> Segment
	segments map[string]map[string]map[string]*record.Segment
}

func NewRecorder(roomId string) record.IRecorder {
	currentTime := time.Now()
	outputDir := "./recordings/" + roomId + "/" + currentTime.Format("20060102-150405")
	os.MkdirAll(outputDir, 0755)
	return &Recorder{
		roomId: roomId,
		packetChannels: make(map[string]chan *rtp.Packet),
		stop:     make(chan struct{}),
		cancelFuncs: make(map[string]context.CancelFunc),
		outputDir: outputDir,
		startTime: currentTime,
		segments: make(map[string]map[string]map[string]*record.Segment),
	}
}

func (r *Recorder) Start(tracks map[string]map[string]*webrtc.TrackRemote) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.createSegments(tracks, r.startTime)
	for userId, userTracks := range tracks {
		for _, t := range userTracks {
			r.startRecord(userId, t)
		}
	}
}

func (r *Recorder) Stop() {
	now := time.Now()
	r.mu.Lock()
	for userId, userSegs := range r.segments {
		for streamId, segs := range userSegs {
			for trackId, seg := range segs {
				if seg.EndTime.IsZero() {
					seg.EndTime = now
					r.segments[userId][streamId][trackId] = seg
				}
			}
		}
	}
	r.endTime = now
	r.mu.Unlock()

	close(r.stop)
	r.wg.Wait()
	r.MergeWithBlanksToMP4()
}

func (r *Recorder) GetPacketChannels() map[string]chan *rtp.Packet {
	return r.packetChannels
}

func (r *Recorder) createSegments(
    tracks map[string]map[string]*webrtc.TrackRemote,
    startTime time.Time,
) map[string]map[string]map[string]*record.Segment {
    segments := make(map[string]map[string]map[string]*record.Segment)

    for userId, userTracks := range tracks {
        if _, exists := segments[userId]; !exists {
            segments[userId] = make(map[string]map[string]*record.Segment)
        }
		r.segments[userId] = make(map[string]map[string]*record.Segment)
        for _, t := range userTracks {
			if _, exists := r.segments[userId][t.StreamID()]; !exists {
				r.segments[userId][t.StreamID()] = make(map[string]*record.Segment)
			}
            r.segments[userId][t.StreamID()][t.ID()] = record.NewSegment(userId, t, startTime)
        }
    }

    return segments
}

func (r *Recorder) startRecord(userId string, t *webrtc.TrackRemote) {
	log.Println("Starting recording for track ID:", t.ID(), "StreamID:", t.StreamID())
    ctx, cancel := context.WithCancel(context.Background())
	r.cancelFuncs[t.ID()] = cancel
	path := r.outputDir + "/" + userId + "/" + t.StreamID() + "/"
	os.MkdirAll(path, 0755)
	filename := t.ID()
	if t.Kind() == webrtc.RTPCodecTypeVideo {
		r.packetChannels[t.ID()] = make(chan *rtp.Packet, 4096)
		r.wg.Add(1)
		go r.record(ctx, t, NewIVFWriter(path + filename + ".ivf", t.Codec().MimeType))
	}
	if t.Kind() == webrtc.RTPCodecTypeAudio {
		r.packetChannels[t.ID()] = make(chan *rtp.Packet, 4096)
		r.wg.Add(1)
		go r.record(ctx, t, NewOggWriter(path + filename + ".ogg"))
	}
}

func (r *Recorder) record(ctx context.Context, t *webrtc.TrackRemote, writer RTPWriter) {
	defer r.wg.Done()
	for {
		select {
		case <-r.stop:
			writer.Close()
			return
		case <-ctx.Done():
			writer.Close()
			return
		case pkt, ok := <-r.packetChannels[t.ID()]:
			if !ok {
				return
			}
			writer.WriteRTP(pkt)
		}
	}
}

func (r *Recorder) AddParticipant(userId string, t *webrtc.TrackRemote) {
	if r.segments[userId] == nil {
		r.segments[userId] = make(map[string]map[string]*record.Segment)
	}
	if r.segments[userId][t.StreamID()] == nil {
		r.segments[userId][t.StreamID()] = make(map[string]*record.Segment)
	}
	r.segments[userId][t.StreamID()][t.ID()] = record.NewSegment(userId, t, time.Time{})
	r.startRecord(userId, t)
}

func (r *Recorder) RemoveParticipant(userId string, t *webrtc.TrackRemote) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// cancel recording goroutine
	if cancel, ok := r.cancelFuncs[t.ID()]; ok {
		cancel()
		delete(r.cancelFuncs, t.ID())
	}

	// close packet channel
	delete(r.packetChannels, t.ID())

	// set EndTime instead of deleting segment
	if userSegs, ok := r.segments[userId]; ok {
		if streamSegs, exists := userSegs[t.StreamID()]; exists {
			if seg, exists := streamSegs[t.ID()]; exists {
				seg.EndTime = time.Now()
				r.segments[userId][t.StreamID()][t.ID()] = seg
			}
		}
	}
}

// func (r *Recorder) MergeAllToMP4() error {

// 	pairs := record.CollectMediaPairs(r.segments, r.outputDir)
// 	if len(pairs) == 0 {
// 		return fmt.Errorf("no tracks")
// 	}

// 	// ---- single ----
// 	if len(pairs) == 1 {
// 		args := []string{"-y"}
// 		ok := record.AddFFmpegInputs(&args, pairs[0], 0, 0)
// 		if !ok {
// 			return fmt.Errorf("no valid media")
// 		}

// 		args = append(args,
// 			"-c:v", "libx264", "-preset", "fast", "-crf", "23",
// 			"-c:a", "aac", "-b:a", "128k",
// 			"-movflags", "+faststart",
// 			filepath.Join(r.outputDir, "merged.mp4"),
// 		)

// 		cmd := exec.Command("ffmpeg", args...)
// 		cmd.Stdout = os.Stdout
// 		cmd.Stderr = os.Stderr
// 		return cmd.Run()
// 	}

// 	// ---- multi ----
// 	args := []string{"-y"}
// 	type inputIndex struct{ v, a int }
// 	inputs := []record.InputIndex{}
// 	idx := 0

// 	for _, p := range pairs {
// 		if record.AddFFmpegInputs(&args, p, 0, 0) {
// 			inputs = append(inputs, record.InputIndex{V: idx, A: idx + 1})
// 			idx += 2
// 		}
// 	}

// 	n := len(inputs)
// 	if n == 0 {
// 		return fmt.Errorf("no valid inputs")
// 	}

// 	filter := record.CreateLayout(inputs, 0, 0)

// 	args = append(args,
// 		"-filter_complex", filter,
// 		"-map", "[v]",
// 		"-map", "[a]",
// 		"-c:v", "libx264",
// 		"-preset", "fast",
// 		"-crf", "23",
// 		"-c:a", "aac",
// 		"-b:a", "128k",
// 		"-movflags", "+faststart",
// 		filepath.Join(r.outputDir, "merged.mp4"),
// 	)

// 	cmd := exec.Command("ffmpeg", args...)
// 	cmd.Stdout = os.Stdout
// 	cmd.Stderr = os.Stderr
// 	return cmd.Run()
// }

func (r *Recorder) MergeWithBlanksToMP4() error {
	// 各ユーザーごとに body.mp4 を作るだけ
	userVideos := []string{}

	for userId, userSegs := range r.segments {
		if len(userSegs) == 0 {
			continue
		}
		mediaPairs := []record.MediaPair{}
		for streamId, segs := range userSegs {
			mediaPair := record.MediaPair{
				Video: "",
				Audio: "",
			}
			for trackId, seg := range segs {
				if seg.EndTime.IsZero() {
					continue
				}
				if mediaPair.StartTime.IsZero() || seg.StartTime.Before(mediaPair.StartTime) {
					mediaPair.StartTime = seg.StartTime
				}
				if mediaPair.EndTime.IsZero() || seg.EndTime.After(mediaPair.EndTime) {
					mediaPair.EndTime = seg.EndTime
				}
				base := filepath.Join(r.outputDir, userId, streamId)
				if seg.Kind == webrtc.RTPCodecTypeVideo {
					mediaPair.Video = filepath.Join(base, trackId+".ivf")
				}
				if seg.Kind == webrtc.RTPCodecTypeAudio {
					mediaPair.Audio = filepath.Join(base, trackId+".ogg")
				}
			}
			if mediaPair.Video == "" && mediaPair.Audio == "" {
				continue
			}
			mediaPairs = append(mediaPairs, mediaPair)
		}
		if len(mediaPairs) == 0 {
			continue
		}
		// if len(mediaPairs) == 1 {
		// 	//　TODO: 途中参加・途中退室の処理
		// 	out := filepath.Join(r.outputDir, userId, userId+"_body_1.mp4")
		// 	// out := filepath.Join(r.outputDir, userId, userId+"_body"+strconv.Itoa(index)+".mp4")
		// 	if err := buildBodyMP4(out, mediaPairs[0]); err != nil {
		// 		return err
		// 	}
		// 	userVideos = append(userVideos, out)
		// 	continue
		// }

		// 途中参加・途中退室をconcat
		lastStartTime := r.startTime
		list := filepath.Join(r.outputDir, userId, "concat_list.txt")
		files := []string{}
		const joinTolerance = 300 * time.Millisecond
		const endTolerance  = 300 * time.Millisecond

		for i, mp := range mediaPairs {

			// ---------- 途中参加（前の区間との gap） ----------
			if mp.StartTime.Sub(lastStartTime) > joinTolerance {
				blackOut := filepath.Join(
					r.outputDir,
					userId,
					userId+"_preblack_"+strconv.Itoa(i)+".mp4",
				)

				buildBlackMP4(
					blackOut,
					mp.StartTime.Sub(lastStartTime),
					640,
					480,
				)

				files = append(files, blackOut)
			}

			// ---------- 本体 ----------
			out := filepath.Join(
				r.outputDir,
				userId,
				userId+"_body_"+strconv.Itoa(i)+".mp4",
			)

			if err := buildBodyMP4(out, mp); err != nil {
				return err
			}

			files = append(files, out)

			// ---------- last mp のみ：退出後 black ----------
			if i == len(mediaPairs)-1 {

				diff := r.endTime.Sub(mp.EndTime)

				if diff > endTolerance {
					// 退出後〜録画終了まで black
					blackOut := filepath.Join(
						r.outputDir,
						userId,
						userId+"_postblack.mp4",
					)

					buildBlackMP4(
						blackOut,
						diff,
						640,
						480,
					)

					files = append(files, blackOut)
				}
			}

			// 次の比較用
			lastStartTime = mp.EndTime
		}
		if err := writeConcatList(list, files); err != nil {
			return err
		}
		out := filepath.Join(r.outputDir, userId, userId+"_body_new.mp4")
		ffmpegConcatReencode(list, out)
		userVideos = append(userVideos, out)
	}

	if len(userVideos) == 0 {
		return fmt.Errorf("no user videos")
	}

	// 1人ならそのまま
	if len(userVideos) == 1 {
		return ffmpegCopy(userVideos[0], filepath.Join(r.outputDir, "merge_all.mp4"))
	}

	// 複数人 → レイアウト合成
	return mergeTimelinesToGrid(
		userVideos,
		filepath.Join(r.outputDir, "merge_all.mp4"),
	)
}

func ffmpegConcatReencode(listFile, out string) error {
	// concat demuxer -> 再エンコードで確実に（copyは環境で壊れやすい）
	args := []string{
		"-y",
		"-f", "concat",
		"-safe", "0",
		"-i", listFile,
		"-c:v", "libx264", "-preset", "fast", "-crf", "23",
		"-c:a", "aac", "-b:a", "128k",
		"-movflags", "+faststart",
		out,
	}
	cmd := exec.Command("ffmpeg", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func writeConcatList(path string, files []string) error {
	if err := ensureDir(filepath.Dir(path)); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	for _, p := range files {
		abs, _ := filepath.Abs(p)
		fmt.Fprintf(f, "file '%s'\n", abs)
	}
	return nil
}

func mergeTimelinesToGrid(timelines []string, out string) error {
	n := len(timelines)
	if n == 0 {
		return fmt.Errorf("no inputs")
	}

	// n==1 のとき xstack は使えない（inputs>=2）。ここがあなたの「同じミス多い」の原因。
	if n == 1 {
		// 単体はそのまま出力（copyでOK）
		return ffmpegCopy(timelines[0], out)
	}

	args := []string{"-y"}
	for _, p := range timelines {
		args = append(args, "-i", p)
	}

	// mp4 1本につき input index は 0..n-1、videoもaudioも同じ index を参照する
	inputs := make([]record.InputIndex, 0, n)
	for i := 0; i < n; i++ {
		inputs = append(inputs, record.InputIndex{V: i, A: i})
	}

	filter := record.CreateLayoutForTimelines(inputs, 0, 0) // 下の record 側を参照

	args = append(args,
		"-filter_complex", filter,
		"-map", "[v]",
		"-map", "[a]",
		"-c:v", "libx264",
		"-preset", "fast",
		"-crf", "23",
		"-c:a", "aac",
		"-b:a", "128k",
		"-movflags", "+faststart",
		out,
	)

	cmd := exec.Command("ffmpeg", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func buildBodyMP4(out string, pair record.MediaPair) error {
	args := []string{"-y"}
	ok := record.AddFFmpegInputs(&args, pair, 0, 0)
	if !ok {
		return fmt.Errorf("no valid media for body: %+v", pair)
	}

	args = append(args,
		"-c:v", "libx264", "-preset", "fast", "-crf", "23",
		"-c:a", "aac", "-b:a", "128k",
		"-movflags", "+faststart",
		out,
	)

	cmd := exec.Command("ffmpeg", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func buildBlackMP4(out string, dur time.Duration, w, h int) error {
	secs := dur.Seconds()
	if secs < 0 {
		secs = 0
	}
	args := []string{
		"-y",
		"-f", "lavfi", "-i", fmt.Sprintf("color=size=%dx%d:rate=30:color=black", w, h),
		"-f", "lavfi", "-i", "anullsrc=channel_layout=stereo:sample_rate=48000",
		"-t", fmt.Sprintf("%.3f", secs),
		"-c:v", "libx264", "-preset", "fast", "-crf", "23",
		"-c:a", "aac", "-b:a", "128k",
		"-movflags", "+faststart",
		out,
	}
	cmd := exec.Command("ffmpeg", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func ffmpegCopy(in, out string) error {
	args := []string{"-y", "-i", in, "-c", "copy", "-movflags", "+faststart", out}
	cmd := exec.Command("ffmpeg", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func ensureDir(dir string) error {
	return os.MkdirAll(dir, 0755)
}
