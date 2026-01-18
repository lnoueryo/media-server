package ondemand

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"streaming-media.jounetsism.biz/record"
)

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

func ffmpegGenerateThumbnail(
	in string,
	thumbPath string,
) error {
	if err := os.MkdirAll(filepath.Dir(thumbPath), 0755); err != nil {
		return err
	}

	args := []string{
		"-y",
		"-ss", "1",
		"-i", in,
		"-frames:v", "1",
		thumbPath,
	}

	cmd := exec.Command("ffmpeg", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func ffmpegGenerateHLS(
	in string,
	hlsDir string,
) error {
	if err := os.MkdirAll(hlsDir, 0755); err != nil {
		return err
	}

	hlsIndex := filepath.Join(hlsDir, "index.m3u8")
	hlsSegment := filepath.Join(hlsDir, "segment_%03d.ts")

	args := []string{
		"-y",
		"-i", in,
		"-c:v", "libx264",
		"-c:a", "aac",
		"-hls_time", "4",
		"-hls_playlist_type", "vod",
		"-hls_segment_filename", hlsSegment,
		hlsIndex,
	}

	cmd := exec.Command("ffmpeg", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func ensureDir(dir string) error {
	return os.MkdirAll(dir, 0755)
}