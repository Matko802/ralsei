package audio

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"math"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/hraban/opus"
)

const (
	SampleRate = 48000
	FrameSize  = 960
)

type Sound struct {
	Frames [][]byte
}

func square(freq, ms, volume float64) []int16 {
	n := int(SampleRate * ms / 1000)
	out := make([]int16, n)
	period := float64(SampleRate) / freq
	for i := 0; i < n; i++ {
		v := -1.0
		if math.Mod(float64(i), period) < period/2 {
			v = 1.0
		}
		out[i] = int16(v * volume * 32767)
	}
	return out
}

func sweep(from, to, ms, volume float64) []int16 {
	n := int(SampleRate * ms / 1000)
	out := make([]int16, n)
	phase := 0.0
	for i := 0; i < n; i++ {
		t := float64(i) / float64(n)
		freq := from + (to-from)*t
		phase += freq / SampleRate
		v := -1.0
		if phase-math.Floor(phase) < 0.5 {
			v = 1.0
		}
		env := 1 - t
		out[i] = int16(v * volume * env * 32767)
	}
	return out
}

func sine(freq, ms, volume float64) []int16 {
	n := int(SampleRate * ms / 1000)
	out := make([]int16, n)
	for i := 0; i < n; i++ {
		t := float64(i) / SampleRate
		env := 1 - float64(i)/float64(n)
		out[i] = int16(math.Sin(2*math.Pi*freq*t) * volume * env * 32767)
	}
	return out
}

func concat(parts ...[]int16) []int16 {
	total := 0
	for _, p := range parts {
		total += len(p)
	}
	out := make([]int16, 0, total)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func silence(ms int) []int16 {
	return make([]int16, SampleRate*ms/1000)
}

func encode(pcm []int16) [][]byte {
	enc, err := opus.NewEncoder(SampleRate, 1, opus.AppVoIP)
	if err != nil {
		return nil
	}
	var frames [][]byte
	for i := 0; i < len(pcm); i += FrameSize {
		chunk := make([]int16, FrameSize)
		copy(chunk, pcm[i:])
		buf := make([]byte, 4000)
		n, err := enc.Encode(chunk, buf)
		if err != nil {
			continue
		}
		frames = append(frames, buf[:n])
	}
	return frames
}

func resample(pcm []int16, from, to int) []int16 {
	if from == to {
		return pcm
	}
	n := int(float64(len(pcm)) * float64(to) / float64(from))
	out := make([]int16, n)
	ratio := float64(from) / float64(to)
	for i := range out {
		pos := float64(i) * ratio
		i0 := int(pos)
		i1 := i0 + 1
		if i1 >= len(pcm) {
			i1 = len(pcm) - 1
		}
		frac := pos - float64(i0)
		out[i] = int16(float64(pcm[i0])*(1-frac) + float64(pcm[i1])*frac)
	}
	return out
}

func voiceSampleRate(model string) int {
	data, err := os.ReadFile(model + ".json")
	if err != nil {
		return 22050
	}
	var meta struct {
		Audio struct {
			SampleRate int `json:"sample_rate"`
		} `json:"audio"`
	}
	if json.Unmarshal(data, &meta) != nil || meta.Audio.SampleRate == 0 {
		return 22050
	}
	return meta.Audio.SampleRate
}

func TTSPCM(text, bin, model string) ([]int16, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--model", model, "--output_raw")
	cmd.Stdin = strings.NewReader(text)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return nil, 0, err
	}
	raw := out.Bytes()
	pcm := make([]int16, len(raw)/2)
	for i := range pcm {
		pcm[i] = int16(binary.LittleEndian.Uint16(raw[i*2:]))
	}
	return pcm, voiceSampleRate(model), nil
}

func FromPCM(pcm []int16, rate int) Sound {
	return Sound{Frames: encode(resample(pcm, rate, SampleRate))}
}

var (
	once   sync.Once
	cache  map[string]Sound
	sounds = map[string]func() []int16{
		"menu": func() []int16 {
			return square(880, 50, 0.18)
		},
		"select": func() []int16 {
			return square(660, 40, 0.12)
		},
		"hit": func() []int16 {
			return concat(sweep(520, 110, 160, 0.5), silence(40))
		},
		"miss": func() []int16 {
			return concat(square(110, 120, 0.3), silence(60))
		},
		"act": func() []int16 {
			return concat(sine(660, 70, 0.25), sine(880, 90, 0.2))
		},
		"win": func() []int16 {
			return concat(sine(523, 90, 0.3), sine(659, 90, 0.3), sine(784, 140, 0.3))
		},
		"continue": func() []int16 {
			return concat(sine(392, 90, 0.3), sine(523, 120, 0.3))
		},
		"flee": func() []int16 {
			return sweep(900, 180, 180, 0.35)
		},
		"event": func() []int16 {
			return concat(sweep(300, 900, 120, 0.25), silence(30), square(900, 50, 0.15))
		},
	}
)

func Get(name string) Sound {
	once.Do(func() {
		cache = make(map[string]Sound, len(sounds))
		for name, gen := range sounds {
			cache[name] = Sound{Frames: encode(gen())}
		}
	})
	return cache[name]
}
