package main

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Config struct {
	Local       int               `json:"local"`
	Destination string            `json:"destination"`
	Config      ConfigTuning      `json:"config"`
	Servers     map[string]Server `json:"servers"`
}

type ConfigTuning struct {
	Parallel int `json:"parallel"`
}

type Server struct {
	IP   string `json:"ip"`
	Port int    `json:"port"`
}

type fileTask struct {
	abs  string
	rel  string
	size int64
}

func main() {
	flag.Parse()
	cfg, err := loadConfig("fastsend.json")
	if err != nil {
		fatalf("load config: %v", err)
	}
	if flag.NArg() == 0 {
		if err := runServer(cfg); err != nil {
			fatalf("server: %v", err)
		}
		return
	}
	if err := runClient(cfg, flag.Args()); err != nil {
		fatalf("client: %v", err)
	}
}

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if cfg.Config.Parallel <= 0 {
		cfg.Config.Parallel = runtime.NumCPU()
		if cfg.Config.Parallel < 1 {
			cfg.Config.Parallel = 1
		}
	}
	if cfg.Destination == "" {
		cfg.Destination = "test-target"
	}
	return &cfg, nil
}

func runServer(cfg *Config) error {
	addr := fmt.Sprintf(":%d", cfg.Local)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	fmt.Printf("listening on %s\n", addr)
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Temporary() {
				continue
			}
			return err
		}
		fmt.Printf("incoming connection from %s\n", conn.RemoteAddr())
		go handleConn(conn, cfg.Destination)
	}
}

func handleConn(conn net.Conn, dest string) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)
	for {
		pathLen, err := readU32(r)
		if err != nil {
			return
		}
		if pathLen == 0 {
			return
		}
		size, err := readU64(r)
		if err != nil {
			return
		}
		pathBytes := make([]byte, pathLen)
		if _, err := io.ReadFull(r, pathBytes); err != nil {
			return
		}
		rel := filepath.Clean(string(pathBytes))
		if strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
			if err := discardAndSkip(r, w, rel, size); err != nil {
				return
			}
			continue
		}
		outPath := filepath.Join(dest, rel)
		if _, err := os.Stat(outPath); err == nil {
			if err := discardAndSkip(r, w, rel, size); err != nil {
				return
			}
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			if err := discardAndSkip(r, w, rel, size); err != nil {
				return
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
			if err := discardAndSkip(r, w, rel, size); err != nil {
				return
			}
			continue
		}
		f, err := os.Create(outPath)
		if err != nil {
			if err := discardAndSkip(r, w, rel, size); err != nil {
				return
			}
			continue
		}
		_, copyErr := io.CopyN(f, r, int64(size))
		closeErr := f.Close()
		if copyErr != nil {
			return
		}
		if closeErr != nil {
			return
		}
		fmt.Printf("completed %s (%d bytes)\n", rel, size)
		_ = writeStatus(w, statusOK)
	}
}

func runClient(cfg *Config, args []string) error {
	target, files := args[0], args[1:]
	if len(files) == 0 {
		return errors.New("no files or directories provided")
	}
	addr, err := resolveServer(cfg, target)
	if err != nil {
		return err
	}
	fmt.Printf("connecting to %s\n", addr)
	tasks, err := collectTasks(files)
	if err != nil {
		return err
	}
	if len(tasks) == 0 {
		return errors.New("no files found")
	}
	workers := cfg.Config.Parallel
	if workers < 1 {
		workers = 1
	}
	var sentBytes int64
	start := time.Now()
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		var last int64
		lastTime := time.Now()
		for {
			select {
			case <-ticker.C:
				total := atomic.LoadInt64(&sentBytes)
				delta := total - last
				last = total
				now := time.Now()
				elapsed := now.Sub(lastTime)
				if elapsed <= 0 {
					elapsed = time.Nanosecond
				}
				lastTime = now
				fmt.Printf("transfer rate %s\n", formatRateFloat(float64(delta)/elapsed.Seconds()))
			case <-done:
				return
			}
		}
	}()
	ch := make(chan fileTask, workers*2)
	var wg sync.WaitGroup
	var sendErr error
	var mu sync.Mutex
	var connectedOnce sync.Once
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := net.Dial("tcp", addr)
			if err != nil {
				setErr(&mu, &sendErr, err)
				return
			}
			connectedOnce.Do(func() {
				fmt.Printf("connected to %s\n", addr)
			})
			defer conn.Close()
			w := bufio.NewWriterSize(&countingWriter{w: conn, counter: &sentBytes}, 1<<20)
			r := bufio.NewReader(conn)
			buf := make([]byte, 1<<20)
			for task := range ch {
				if sendErr != nil {
					return
				}
				if err := writeFile(w, task, buf); err != nil {
					setErr(&mu, &sendErr, err)
					return
				}
				if err := w.Flush(); err != nil {
					setErr(&mu, &sendErr, err)
					return
				}
				status, err := readStatus(r)
				if err != nil {
					setErr(&mu, &sendErr, err)
					return
				}
				if status == statusSkipped {
					fmt.Printf("skipped %s\n", task.rel)
				} else {
					fmt.Printf("completed %s\n", task.rel)
				}
			}
			_ = writeU32(w, 0)
			_ = w.Flush()
		}()
	}
	for _, task := range tasks {
		if sendErr != nil {
			break
		}
		ch <- task
	}
	close(ch)
	wg.Wait()
	close(done)
	elapsed := time.Since(start)
	if elapsed <= 0 {
		elapsed = time.Nanosecond
	}
	total := atomic.LoadInt64(&sentBytes)
	fmt.Printf("sent %s in %s (%s)\n", formatBytes(total), elapsed.Round(time.Millisecond), formatRateFloat(float64(total)/elapsed.Seconds()))
	return sendErr
}

func setErr(mu *sync.Mutex, target *error, err error) {
	mu.Lock()
	defer mu.Unlock()
	if *target == nil {
		*target = err
	}
}

func resolveServer(cfg *Config, target string) (string, error) {
	if s, ok := cfg.Servers[target]; ok {
		return fmt.Sprintf("%s:%d", s.IP, s.Port), nil
	}
	if strings.Contains(target, ":") {
		return target, nil
	}
	if cfg.Local == 0 {
		return "", errors.New("missing port for server target")
	}
	return fmt.Sprintf("%s:%d", target, cfg.Local), nil
}

func collectTasks(inputs []string) ([]fileTask, error) {
	var tasks []fileTask
	for _, input := range inputs {
		info, err := os.Stat(input)
		if err != nil {
			return nil, err
		}
		if info.IsDir() {
			rootBase := filepath.Base(input)
			err = filepath.WalkDir(input, func(path string, d os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() {
					return nil
				}
				fi, err := d.Info()
				if err != nil {
					return err
				}
				if !fi.Mode().IsRegular() {
					return nil
				}
				relPath, err := filepath.Rel(input, path)
				if err != nil {
					return err
				}
				tasks = append(tasks, fileTask{
					abs:  path,
					rel:  filepath.Join(rootBase, relPath),
					size: fi.Size(),
				})
				return nil
			})
			if err != nil {
				return nil, err
			}
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}
		tasks = append(tasks, fileTask{
			abs:  input,
			rel:  filepath.Base(input),
			size: info.Size(),
		})
	}
	return tasks, nil
}

func writeFile(w *bufio.Writer, task fileTask, buf []byte) error {
	if err := writeU32(w, uint32(len(task.rel))); err != nil {
		return err
	}
	if err := writeU64(w, uint64(task.size)); err != nil {
		return err
	}
	if _, err := w.WriteString(task.rel); err != nil {
		return err
	}
	f, err := os.Open(task.abs)
	if err != nil {
		return err
	}
	_, err = io.CopyBuffer(w, f, buf)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

type countingWriter struct {
	w       io.Writer
	counter *int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	if n > 0 {
		atomic.AddInt64(c.counter, int64(n))
	}
	return n, err
}

func discardAndSkip(r io.Reader, w *bufio.Writer, rel string, size uint64) error {
	if _, err := io.CopyN(io.Discard, r, int64(size)); err != nil {
		return err
	}
	fmt.Printf("skipped %s (%d bytes)\n", rel, size)
	return writeStatus(w, statusSkipped)
}

const (
	statusOK      byte = 0
	statusSkipped byte = 1
)

func writeStatus(w *bufio.Writer, status byte) error {
	if err := w.WriteByte(status); err != nil {
		return err
	}
	return w.Flush()
}

func readStatus(r *bufio.Reader) (byte, error) {
	return r.ReadByte()
}

func readU32(r io.Reader) (uint32, error) {
	var b [4]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(b[:]), nil
}

func readU64(r io.Reader) (uint64, error) {
	var b [8]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint64(b[:]), nil
}

func writeU32(w io.Writer, v uint32) error {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	_, err := w.Write(b[:])
	return err
}

func writeU64(w io.Writer, v uint64) error {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	_, err := w.Write(b[:])
	return err
}

func formatBytes(v int64) string {
	if v < 1024 {
		return fmt.Sprintf("%d B", v)
	}
	return formatScaled(float64(v), "B")
}

func formatRate(v int64) string {
	if v < 1024 {
		return fmt.Sprintf("%d B/s", v)
	}
	return formatScaled(float64(v), "B/s")
}

func formatRateFloat(v float64) string {
	if v < 1024 {
		return fmt.Sprintf("%.0f B/s", v)
	}
	return formatScaled(v, "B/s")
}

func formatScaled(v float64, suffix string) string {
	units := []string{"k", "M", "G", "T"}
	for _, unit := range units {
		v /= 1024
		if v < 1024 {
			if v >= 100 {
				return fmt.Sprintf("%.0f %s%s", v, unit, suffix)
			}
			if v >= 10 {
				return fmt.Sprintf("%.1f %s%s", v, unit, suffix)
			}
			return fmt.Sprintf("%.2f %s%s", v, unit, suffix)
		}
	}
	return fmt.Sprintf("%.2f P%s", v/1024, suffix)
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
