package fastping

import (
	"encoding/binary"
	"net"
	"sync"
	"testing"
	"time"
)

type addHostTest struct {
	host   string
	addr   *net.IPAddr
	expect bool
}

var addHostTests = []addHostTest{
	{host: "127.0.0.1", addr: &net.IPAddr{IP: net.IPv4(127, 0, 0, 1)}, expect: true},
	{host: "localhost", addr: &net.IPAddr{IP: net.IPv4(127, 0, 0, 1)}, expect: false},
}

func TestAddIP(t *testing.T) {
	p := NewPinger()

	for _, tt := range addHostTests {
		if ok := p.AddIP(tt.host); ok != nil {
			if tt.expect != false {
				t.Errorf("AddIP failed: got %v, expected %v", ok, tt.expect)
			}
		}
	}
	for _, tt := range addHostTests {
		if tt.expect {
			if !p.addrs[tt.host].IP.Equal(tt.addr.IP) {
				t.Errorf("AddIP didn't save IPAddr: %v", tt.host)
			}
		}
	}
}

func TestRun(t *testing.T) {
	p := NewPinger()

	if err := p.AddIP("127.0.0.1"); err != nil {
		t.Fatalf("AddIP failed: %v", err)
	}

	if err := p.AddIP("127.0.0.100"); err != nil {
		t.Fatalf("AddIP failed: %v", err)
	}

	if err := p.AddIP("::1"); err != nil {
		t.Fatalf("AddIP failed: %v", err)
	}

	found1, found100, foundv6 := false, false, false
	called, idle := false, false
	p.OnRecv = func(ip *net.IPAddr, d time.Duration) {
		called = true
		if ip.String() == "127.0.0.1" {
			found1 = true
		} else if ip.String() == "127.0.0.100" {
			found100 = true
		} else if ip.String() == "::1" {
			foundv6 = true
		}
	}

	p.OnIdle = func() {
		idle = true
	}

	err := p.Run()
	if err != nil {
		t.Fatalf("Pinger returns error: %v", err)
	}
	if !called {
		t.Fatalf("Pinger didn't get any responses")
	}
	if !idle {
		t.Fatalf("Pinger didn't call OnIdle function")
	}
	if !found1 {
		t.Fatalf("Pinger `127.0.0.1` didn't respond")
	}
	if !found100 {
		t.Fatalf("Pinger `127.0.0.100` didn't respond")
	}
	if !foundv6 {
		t.Fatalf("Pinger `::1` didn't responded")
	}
}

func TestMultiRun(t *testing.T) {
	p1 := NewPinger()
	p2 := NewPinger()

	if err := p1.AddIP("127.0.0.1"); err != nil {
		t.Fatalf("AddIP 1 failed: %v", err)
	}

	if err := p2.AddIP("127.0.0.1"); err != nil {
		t.Fatalf("AddIP 2 failed: %v", err)
	}

	var mu sync.Mutex
	res1 := 0
	p1.OnRecv = func(*net.IPAddr, time.Duration) {
		mu.Lock()
		res1++
		mu.Unlock()
	}

	res2 := 0
	p2.OnRecv = func(*net.IPAddr, time.Duration) {
		mu.Lock()
		res2++
		mu.Unlock()
	}

	p1.MaxRTT, p2.MaxRTT = time.Millisecond*100, time.Millisecond*100

	if err := p1.Run(); err != nil {
		t.Fatalf("Pinger 1 returns error: %v", err)
	}
	if res1 == 0 {
		t.Fatalf("Pinger 1 didn't get any responses")
	}
	if res2 > 0 {
		t.Fatalf("Pinger 2 got response")
	}

	res1, res2 = 0, 0
	if err := p2.Run(); err != nil {
		t.Fatalf("Pinger 2 returns error: %v", err)
	}
	if res1 > 0 {
		t.Fatalf("Pinger 1 got response")
	}
	if res2 == 0 {
		t.Fatalf("Pinger 2 didn't get any responses")
	}

	res1, res2 = 0, 0
	errch1, errch2 := make(chan error), make(chan error)
	go func(ch chan error) {
		err := p1.Run()
		if err != nil {
			ch <- err
		}
	}(errch1)
	go func(ch chan error) {
		err := p2.Run()
		if err != nil {
			ch <- err
		}
	}(errch2)
	ticker := time.NewTicker(time.Millisecond * 250)
	select {
	case err := <-errch1:
		t.Fatalf("Pinger 1 returns error: %v", err)
	case err := <-errch2:
		t.Fatalf("Pinger 2 returns error: %v", err)
	case <-ticker.C:
		break
	}
	mu.Lock()
	defer mu.Unlock()
	if res1 != 2 {
		t.Fatalf("Pinger 1 didn't get correct response: %v", res1, res2)
	}
	if res2 != 2 {
		t.Fatalf("Pinger 2 didn't get correct response: %v", res1, res2)
	}
}

func TestRunLoop(t *testing.T) {
	p := NewPinger()

	if err := p.AddIP("127.0.0.1"); err != nil {
		t.Fatalf("AddIP failed: %v", err)
	}
	p.MaxRTT = time.Millisecond * 100

	recvCount, idleCount := 0, 0
	p.OnRecv = func(*net.IPAddr, time.Duration) {
		recvCount++
	}

	p.OnIdle = func() {
		idleCount++
	}

	var err error
	p.RunLoop()
	ticker := time.NewTicker(time.Millisecond * 300)
	select {
	case <-p.Done():
		if err = p.Err(); err != nil {
			t.Fatalf("Pinger returns error %v", err)
		}
	case <-ticker.C:
		break
	}
	ticker.Stop()
	p.Stop()

	if recvCount < 2 {
		t.Fatalf("Pinger receive count less than 2")
	}
	if idleCount < 2 {
		t.Fatalf("Pinger idle count less than 2")
	}
}

func TestTimeToBytes(t *testing.T) {
	test_time := time.Now()
	nsec := test_time.UnixNano()
	expect := make([]byte, binary.MaxVarintLen64)
	binary.PutVarint(expect, nsec)
	b := timeToBytes(test_time)

	for i := 0; i < binary.MaxVarintLen64; i++ {
		if b[i] != expect[i] {
			t.Errorf("timeToBytes failed: got %v, expected: %v", b, expect)
			break
		}
	}
}

func TestBytesToTime(t *testing.T) {
	expect := time.Now()
	nsec := expect.UnixNano()
	b := make([]byte, binary.MaxVarintLen64)
	binary.PutVarint(b, nsec)
	tm := bytesToTime(b)
	if !tm.Equal(expect) {
		t.Errorf("bytesToTime failed: got %v, expected: %v", tm.UTC(), expect.UTC())
	}
}

func TestTimeToBytesToTime(t *testing.T) {
	tm, err := time.Parse(time.RFC3339, "2009-11-10T23:00:00Z")
	if err != nil {
		t.Errorf("time.Parse failed: %v", err)
	}
	b := timeToBytes(tm)
	tm2 := bytesToTime(b)
	if !tm.Equal(tm2) {
		t.Errorf("bytesToTime failed: got %v, expected: %v", tm2.UTC(), tm.UTC())
	}
}

func TestPayloadSizeDefault(t *testing.T) {
	s := timeToBytes(time.Now())
	d := append(s, byteSliceOfSize(TimeSliceLength-TimeSliceLength)...)

	if len(d) != TimeSliceLength {
		t.Errorf("Payload size incorrect: got %d, expected: %d", len(d), TimeSliceLength)
	}
}

func TestPayloadSizeCustom(t *testing.T) {
	s := timeToBytes(time.Now())
	d := append(s, byteSliceOfSize(64-TimeSliceLength)...)

	if len(d) != 64 {
		t.Errorf("Payload size incorrect: got %d, expected: %d", len(d), 64)
	}
}
