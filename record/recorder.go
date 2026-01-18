package record

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

const _cellW, _cellH = 640, 480
const targetRatio = 16.0 / 9.0

type RemoteTracks struct {
	id    string
	video *webrtc.TrackRemote
	audio *webrtc.TrackRemote
}

type MediaPair struct {
	Video string
	Audio string
	StartTime time.Time
	EndTime time.Time
}

type IRecorder interface {
	GetPacketChannels() map[string]chan *rtp.Packet
	Start(trackRemotes map[string]map[string]*webrtc.TrackRemote)
	Stop()
	GetStartUserId() string
	AddParticipant(userId string, t *webrtc.TrackRemote)
	RemoveParticipant(userId string, t *webrtc.TrackRemote)
}

type Segment struct {
	ParticipantID string
	TrackID       string
	StreamID       string
	StartTime     time.Time
	EndTime       time.Time
	Kind 	  webrtc.RTPCodecType
}

type InputIndex struct{ V, A int }

func NewSegment(userId string, t *webrtc.TrackRemote, startTime time.Time) *Segment {
	if startTime.IsZero() {
		startTime = time.Now()
	}
	return &Segment{
		ParticipantID: userId,
		TrackID:       t.ID(),
		StreamID:      t.StreamID(),
		StartTime:     startTime,
		EndTime:       time.Time{},
		Kind: 	  t.Kind(),
	}
}

func CreateLayout(inputs []InputIndex, cellW, cellH int) string {
	if cellW == 0 {
		cellW = _cellW
	}
	if cellH == 0 {
		cellH = _cellH
	}
	n := len(inputs)
	bestCols, bestRows := 1, n
	bestScore := math.MaxFloat64

	for rows := 1; rows <= n; rows++ {
		cols := int(math.Ceil(float64(n) / float64(rows)))
		w := cols * cellW
		h := rows * cellH
		ratio := float64(w) / float64(h)
		score := math.Abs(ratio - targetRatio)
		if score < bestScore {
			bestScore = score
			bestCols = cols
			bestRows = rows
		}
	}

	cols, rows := bestCols, bestRows
	W := cols * cellW
	H := rows * cellH

	finalW, finalH := W, H
	if float64(W)/float64(H) > targetRatio {
		finalH = int(math.Ceil(float64(W) / targetRatio))
	} else {
		finalW = int(math.Ceil(float64(H) * targetRatio))
	}

	var vf []string
	var af []string
	var labels []string

	for i, in := range inputs {
		vf = append(vf,
			fmt.Sprintf("[%d:v]scale=%d:%d,setsar=1[v%d]", in.V, cellW, cellH, i))
		labels = append(labels, fmt.Sprintf("[v%d]", i))
		af = append(af, fmt.Sprintf("[%d:a]", in.A))
	}

	layouts := []string{}
	for r := 0; r < rows; r++ {
		for c := 0; c < cols; c++ {
			i := r*cols + c
			if i >= n {
				break
			}
			layouts = append(layouts,
				fmt.Sprintf("%d_%d", c*cellW, r*cellH))
		}
	}

	return fmt.Sprintf(
		"%s;%s xstack=inputs=%d:layout=%s:fill=black[tmp];"+
			"[tmp]pad=%d:%d:(%d-%d)/2:(%d-%d)/2:black[v];"+
			"%samix=inputs=%d[a]",
		strings.Join(vf, ";"),
		strings.Join(labels, ""),
		n,
		strings.Join(layouts, "|"),
		finalW, finalH,
		finalW, W,
		finalH, H,
		strings.Join(af, ""),
		n,
	)
}

func CollectMediaPairs(segments map[string]map[string]*Segment, outputDir string) []MediaPair {
	pairs := []MediaPair{}

	for userId, userSegments := range segments {
		pair := MediaPair{}
		for trackId, segment := range userSegments {
			base := filepath.Join(outputDir, userId, segment.StreamID)
			if segment.Kind == webrtc.RTPCodecTypeVideo {
				pair.Video = filepath.Join(base, trackId+".ivf")
			} else if segment.Kind == webrtc.RTPCodecTypeAudio {
				pair.Audio = filepath.Join(base, trackId+".ogg")
			}
		}
		if pair.Video != "" || pair.Audio != "" {
			pairs = append(pairs, pair)
		}
	}
	return pairs
}

func AddFFmpegInputs(args *[]string, pair MediaPair, cellW, cellH int) bool {
	if cellW == 0 {
		cellW = _cellW
	}
	if cellH == 0 {
		cellH = _cellH
	}
	vExists := fileExists(pair.Video)
	aExists := fileExists(pair.Audio)

	switch {
	case vExists && aExists:
		*args = append(*args, "-i", pair.Video, "-i", pair.Audio)
		return true
	case vExists:
		*args = append(*args,
			"-i", pair.Video,
			"-f", "lavfi", "-i", "anullsrc=channel_layout=stereo:sample_rate=48000")
		return true
	case aExists:
		*args = append(*args,
			"-f", "lavfi", "-i",
			fmt.Sprintf("color=size=%dx%d:rate=30:color=black", cellW, cellH),
			"-i", pair.Audio)
		return true
	default:
		return false
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func CreateLayoutForTimelines(inputs []InputIndex, cellW, cellH int) string {
	if cellW == 0 {
		cellW = _cellW
	}
	if cellH == 0 {
		cellH = _cellH
	}

	n := len(inputs)
	// n==1 は呼び出し側で弾く（xstack inputs>=2）
	bestCols, bestRows := 1, n
	bestScore := math.MaxFloat64

	for rows := 1; rows <= n; rows++ {
		cols := int(math.Ceil(float64(n) / float64(rows)))
		w := cols * cellW
		h := rows * cellH
		ratio := float64(w) / float64(h)
		score := math.Abs(ratio - targetRatio)
		if score < bestScore {
			bestScore = score
			bestCols = cols
			bestRows = rows
		}
	}

	cols, rows := bestCols, bestRows
	W := cols * cellW
	H := rows * cellH

	finalW, finalH := W, H
	if float64(W)/float64(H) > targetRatio {
		finalH = int(math.Ceil(float64(W) / targetRatio))
	} else {
		finalW = int(math.Ceil(float64(H) * targetRatio))
	}

	var vf []string
	var af []string
	var labels []string

	for i, in := range inputs {
		// PTS を 0 始まりにそろえる（途中参加でも timeline を作ってるので理屈上揃うが、保険で入れる）
		vf = append(vf,
			fmt.Sprintf("[%d:v]setpts=PTS-STARTPTS,scale=%d:%d,setsar=1[v%d]", in.V, cellW, cellH, i))
		labels = append(labels, fmt.Sprintf("[v%d]", i))

		af = append(af,
			fmt.Sprintf("[%d:a]asetpts=PTS-STARTPTS[a%d]", in.A, i))
	}

	layouts := []string{}
	for r := 0; r < rows; r++ {
		for c := 0; c < cols; c++ {
			i := r*cols + c
			if i >= n {
				break
			}
			layouts = append(layouts, fmt.Sprintf("%d_%d", c*cellW, r*cellH))
		}
	}

	// audio: いったん各 a{i} をまとめてから amix
	aRefs := []string{}
	for i := 0; i < n; i++ {
		aRefs = append(aRefs, fmt.Sprintf("[a%d]", i))
	}

	return fmt.Sprintf(
		"%s;%s;%s xstack=inputs=%d:layout=%s:fill=black[tmp];"+
			"[tmp]pad=%d:%d:(%d-%d)/2:(%d-%d)/2:black[v];"+
			"%samix=inputs=%d:normalize=0[a]",
		strings.Join(vf, ";"),
		strings.Join(af, ";"),
		strings.Join(labels, ""),
		n,
		strings.Join(layouts, "|"),
		finalW, finalH,
		finalW, W,
		finalH, H,
		strings.Join(aRefs, ""),
		n,
	)
}