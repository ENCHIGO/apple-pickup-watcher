package notify

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/gopxl/beep/v2"
	"github.com/gopxl/beep/v2/mp3"
	"github.com/gopxl/beep/v2/speaker"
)

const (
	// soundBufferLen 是音频设备缓冲的时长。太小会有卡顿，太大则按下停止后还要响一会儿。
	soundBufferLen = 100 * time.Millisecond
	// soundExtraWait 是在音频自然时长之外多给的等待余量。
	soundExtraWait = 5 * time.Second
)

// Sound 播放内嵌的提示音。
//
// 音频只解码一次并缓存成 PCM，音频设备也只初始化一次；初始化失败会被记下来，
// 之后每次 Notify 返回同一个 error，监控本身不受影响。
type Sound struct {
	mp3 []byte

	once    sync.Once
	initErr error
	buf     *beep.Buffer

	// playing 是一个单槽信号量：同一时刻只允许一次播放在进行。
	playing chan struct{}
}

// NewSound 构造提示音渠道，mp3 是内嵌的音频数据。
// 传入空数据表示没有可用音频，等同于未配置，Notify 会直接返回 nil。
func NewSound(mp3 []byte) *Sound {
	return &Sound{mp3: mp3, playing: make(chan struct{}, 1)}
}

// Name 实现 Notifier。
func (s *Sound) Name() string { return "提示音" }

// Notify 播放一次提示音。通知内容用不上，提示音只负责把人叫过来。
func (s *Sound) Notify(ctx context.Context, _ Notification) error {
	if s == nil || len(s.mp3) == 0 {
		return nil
	}
	if err := s.prepare(); err != nil {
		return err
	}
	return s.play(ctx)
}

// prepare 解码音频并初始化音频设备，整个进程只做一次。
//
// 对应上游 services/listen.go:277-290 的三个问题：
//   - :278-279 每次响铃都从头解码一遍 mp3，解码结果用完即弃，白白重复 CPU 与内存
//     分配；这里解码一次存进 beep.Buffer，之后每次播放只是从内存里读 PCM。
//   - :281 解码失败直接 panic，而 AlertMp3 是在后台 goroutine 里调的，panic 会
//     终止整个进程。
//   - speaker.Init 在上游 main 里无条件调用且不看返回值，机器上没有可用音频设备
//     （无声卡的虚拟机、设备被独占）时初始化其实已经失败，后续播放行为未定义。
//     这里把失败记下来，用户能在界面上看到「提示音不可用」，而监控照常运行。
func (s *Sound) prepare() error {
	s.once.Do(func() {
		// beep 和它底下的 oto 都没有承诺不 panic，音频设备异常时更难说。
		// 这里是唯一会碰到音频驱动的地方，兜一次底。
		defer func() {
			if r := recover(); r != nil {
				s.initErr = fmt.Errorf("初始化提示音时内部错误已被拦截: %v", r)
			}
		}()

		streamer, format, err := mp3.Decode(io.NopCloser(bytes.NewReader(s.mp3)))
		if err != nil {
			s.initErr = fmt.Errorf("解码提示音失败: %w", err)
			return
		}
		defer streamer.Close()

		buf := beep.NewBuffer(format)
		buf.Append(streamer)
		// Streamer 接口不从 Stream 返回错误，解码中途出的错要事后问 Err。
		if err := streamer.Err(); err != nil {
			s.initErr = fmt.Errorf("解码提示音失败: %w", err)
			return
		}
		if buf.Len() == 0 {
			s.initErr = errors.New("提示音音频为空")
			return
		}

		if err := speaker.Init(format.SampleRate, format.SampleRate.N(soundBufferLen)); err != nil {
			s.initErr = fmt.Errorf("初始化音频设备失败: %w", err)
			return
		}
		s.buf = buf
	})
	// sync.Once 保证 Do 返回时上面的写入对所有调用方可见，这里可以直接读。
	return s.initErr
}

// play 播放一遍缓存好的音频，并等待播放结束。
func (s *Sound) play(ctx context.Context) error {
	select {
	case s.playing <- struct{}{}:
	default:
		// 已经在响了。多个目标同一轮到货时叠加播放只会变成噪音，
		// 而且下面超时清理是全局的，两次播放会互相打断。
		return nil
	}
	defer func() { <-s.playing }()

	done := make(chan struct{})
	var closeOnce sync.Once
	streamer := s.buf.Streamer(0, s.buf.Len())
	speaker.Play(beep.Seq(streamer, beep.Callback(func() {
		// 回调由 speaker 的读取 goroutine 调用，close 必须保证只执行一次。
		closeOnce.Do(func() { close(done) })
	})))

	// 正常播完会触发上面的回调，但设备被拔掉、驱动卡死时 mixer 可能再也不来取
	// 数据，回调永远不会执行。上游就是直接 <-done 死等（services/listen.go:289），
	// 那个 goroutine 从此不会退出。这里按音频真实时长加一点余量设上限。
	timeout := s.buf.Format().SampleRate.D(s.buf.Len()) + soundExtraWait
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		// 取消意味着「别响了」，把流从 mixer 里摘掉，而不是让它继续响完。
		speaker.Clear()
		return ctx.Err()
	case <-timer.C:
		// 不清理的话，每次超时都会往 mixer 里堆一条永远不会结束的流。
		speaker.Clear()
		return fmt.Errorf("播放提示音超时（超过 %s）", timeout)
	}
}
