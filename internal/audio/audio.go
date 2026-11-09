package audio

import (
	"math"
	"sync"

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
