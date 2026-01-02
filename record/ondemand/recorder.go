package ondemand

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
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
	segments map[string]map[string]*record.Segment
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
		segments: make(map[string]map[string]*record.Segment),
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
	for userId, segs := range r.segments {
		for trackId, seg := range segs {
			if seg.EndTime.IsZero() {
				seg.EndTime = now
				r.segments[userId][trackId] = seg
			}
		}
	}
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
) map[string]map[string]*record.Segment {
    segments := make(map[string]map[string]*record.Segment)

    for userId, userTracks := range tracks {
        if _, exists := segments[userId]; !exists {
            segments[userId] = make(map[string]*record.Segment)
        }
		r.segments[userId] = make(map[string]*record.Segment)
        for _, t := range userTracks {
            r.segments[userId][t.ID()] = record.NewSegment(userId, t, startTime)
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
		r.segments[userId] = make(map[string]*record.Segment)
	}
	r.segments[userId][t.ID()] = record.NewSegment(userId, t, time.Time{})
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
		if seg, exists := userSegs[t.ID()]; exists {
			seg.EndTime = time.Now()
			r.segments[userId][t.ID()] = seg
		}
	}
}

func (r *Recorder) MergeAllToMP4() error {

	pairs := record.CollectMediaPairs(r.segments, r.outputDir)
	if len(pairs) == 0 {
		return fmt.Errorf("no tracks")
	}

	// ---- single ----
	if len(pairs) == 1 {
		args := []string{"-y"}
		ok := record.AddFFmpegInputs(&args, pairs[0], 0, 0)
		if !ok {
			return fmt.Errorf("no valid media")
		}

		args = append(args,
			"-c:v", "libx264", "-preset", "fast", "-crf", "23",
			"-c:a", "aac", "-b:a", "128k",
			"-movflags", "+faststart",
			filepath.Join(r.outputDir, "merged.mp4"),
		)

		cmd := exec.Command("ffmpeg", args...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}

	// ---- multi ----
	args := []string{"-y"}
	type inputIndex struct{ v, a int }
	inputs := []record.InputIndex{}
	idx := 0

	for _, p := range pairs {
		if record.AddFFmpegInputs(&args, p, 0, 0) {
			inputs = append(inputs, record.InputIndex{V: idx, A: idx + 1})
			idx += 2
		}
	}

	n := len(inputs)
	if n == 0 {
		return fmt.Errorf("no valid inputs")
	}

	filter := record.CreateLayout(inputs, 0, 0)

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
		filepath.Join(r.outputDir, "merged.mp4"),
	)

	cmd := exec.Command("ffmpeg", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (r *Recorder) MergeWithBlanksToMP4() error {
	if r.startTime.IsZero() {
		return fmt.Errorf("global startTime not set")
	}

	// 1) 各ユーザーごとに「開始0:00に揃えた timeline.mp4」を作る（ブランクはファイル作る方式）
	timelineFiles := []string{}

	for userId, tracks := range r.segments {
		userStart := time.Time{}
		for _, seg := range tracks {
			if userStart.IsZero() || seg.StartTime.Before(userStart) {
				userStart = seg.StartTime
			}
		}
		if userStart.IsZero() {
			continue
		}

		// このユーザーの video / audio パス
		vidPath, audPath := "", ""
		for trackId, seg := range tracks {
			base := filepath.Join(r.outputDir, userId, seg.StreamID)
			if seg.Kind == webrtc.RTPCodecTypeVideo {
				vidPath = filepath.Join(base, trackId+".ivf")
			} else if seg.Kind == webrtc.RTPCodecTypeAudio {
				audPath = filepath.Join(base, trackId+".ogg")
			}
		}

		// body.mp4（video/ audio どちらか無くても AddFFmpegInputs で補完）
		userBody := filepath.Join(r.outputDir, userId+"_body.mp4")
		if err := ensureDir(filepath.Dir(userBody)); err != nil {
			return err
		}
		if err := buildBodyMP4(userBody, record.MediaPair{Video: vidPath, Audio: audPath}); err != nil {
			return err
		}

		// ブランク必要なら blank.mp4 作成して concat で timeline.mp4 にする（開始0:00に揃う）
		needBlank := userStart.After(r.startTime)
		userTimeline := filepath.Join(r.outputDir, userId+"_timeline.mp4")

		if !needBlank {
			// timeline = body（絶対パスで統一）
			absBody, _ := filepath.Abs(userBody)
			absTimeline, _ := filepath.Abs(userTimeline)
			if absBody != absTimeline {
				// copy でOK（同じコーデックになるように body は常にx264+aacで作っている）
				if err := ffmpegCopy(absBody, absTimeline); err != nil {
					return err
				}
			}
		} else {
			blankDur := userStart.Sub(r.startTime)
			if blankDur < 0 {
				blankDur = 0
			}

			userBlank := filepath.Join(r.outputDir, userId+"_blank.mp4")
			if err := buildBlankMP4(userBlank, blankDur, 640, 480); err != nil {
				return err
			}

			absBlank, _ := filepath.Abs(userBlank)
			absBody, _ := filepath.Abs(userBody)
			absTimeline, _ := filepath.Abs(userTimeline)

			// concat list（絶対パス）
			listFile := filepath.Join(r.outputDir, userId+"_list.txt")
			if err := writeConcatList(listFile, []string{absBlank, absBody}); err != nil {
				return err
			}

			// concat -> timeline（※ copy は失敗しやすいので再エンコードで確実に）
			if err := ffmpegConcatReencode(listFile, absTimeline); err != nil {
				return err
			}
		}

		absTimeline, _ := filepath.Abs(userTimeline)
		if !fileExists(absTimeline) {
			return fmt.Errorf("timeline not created: %s", absTimeline)
		}
		timelineFiles = append(timelineFiles, absTimeline)
	}

	if len(timelineFiles) == 0 {
		return fmt.Errorf("no timelines")
	}

	// 2) timeline.mp4 を最初から人数分で同時レイアウト合成して merge_all.mp4
	//    ※ concat じゃない。ここが「2人なら2枠」。
	finalOut := filepath.Join(r.outputDir, "merge_all.mp4")
	absOut, _ := filepath.Abs(finalOut)
	if err := mergeTimelinesToGrid(timelineFiles, absOut); err != nil {
		return err
	}
	return nil
}

// ---- helpers ----

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

func buildBlankMP4(out string, dur time.Duration, w, h int) error {
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

func ensureDir(dir string) error {
	return os.MkdirAll(dir, 0755)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}