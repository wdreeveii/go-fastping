// Package fastping is an ICMP ping library inspired by AnyEvent::FastPing Perl
// module to send ICMP ECHO REQUEST packets quickly. Original Perl module is
// available at
// http://search.cpan.org/~mlehmann/AnyEvent-FastPing-2.01/
//
// It hasn't been fully implemented original functions yet.
//
// Here is an example:
//
//	p := fastping.NewPinger()
//	ra, err := net.ResolveIPAddr("ip4:icmp", os.Args[1])
//	if err != nil {
//		fmt.Println(err)
//		os.Exit(1)
//	}
//	p.AddIPAddr(ra)
//	p.OnRecv = func(addr *net.IPAddr, rtt time.Duration) {
//		fmt.Printf("IP Addr: %s receive, RTT: %v\n", addr.String(), rtt)
//	}
//	p.OnIdle = func() {
//		fmt.Println("finish")
//	}
//	err = p.Run()
//	if err != nil {
//		fmt.Println(err)
//	}
//
// It sends an ICMP packet and wait a response. If it receives a response,
// it calls "receive" callback. After that, MaxRTT time passed, it calls
// "idle" callback. If you need more example, please see "cmd/ping/ping.go".
//
// This library needs to run as a superuser for sending ICMP packets so when
// you run go test, please run as a following
//
//	sudo go test
//
package fastping

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"net"
	"sync"
	"syscall"
	"time"
)

const TimeSliceLength = binary.MaxVarintLen64

func byteSliceOfSize(n int) []byte {
	b := make([]byte, n)
	for i := 0; i < len(b); i++ {
		b[i] = 1
	}

	return b
}

func timeToBytes(t time.Time) []byte {
	nsec := t.UnixNano()
	b := make([]byte, binary.MaxVarintLen64)
	binary.PutVarint(b, nsec)
	return b
}

func bytesToTime(b []byte) time.Time {
	var nsec int64
	nsec, _ = binary.Varint(b)
	return time.Unix(nsec/int64(time.Second), nsec%int64(time.Second))
}

func isIPv4(ip net.IP) bool {
	return len(ip.To4()) == net.IPv4len
}

func isIPv6(ip net.IP) bool {
	return len(ip) == net.IPv6len
}

type packet struct {
	msg  *icmpMessage
	addr *net.IPAddr
}

type context struct {
	stop chan bool
	done chan bool
	err  error
}

func newContext() *context {
	return &context{
		stop: make(chan bool),
		done: make(chan bool),
	}
}

// Pinger represents ICMP packet sender/receiver
type Pinger struct {
	id  int
	seq int
	// key string is IPAddr.String()
	addrs map[string]*net.IPAddr
	ctx   *context
	mu    sync.Mutex

	// Size in bytes of the payload to send
	Size int
	// Number of (nano,milli)seconds of an idle timeout. Once it passed,
	// the library calls an idle callback function. It is also used for an
	// interval time of RunLoop() method
	MaxRTT time.Duration
	// OnRecv is called with a response packet's source address and its
	// elapsed time when Pinger receives a response packet.
	OnRecv func(*net.IPAddr, time.Duration)
	// OnIdle is called when MaxRTT time passed
	OnIdle func()
	// If Debug is true, it prints debug messages to stdout.
	Debug bool
}

// NewPinger returns a new Pinger struct pointer
func NewPinger() *Pinger {
	rand.Seed(time.Now().UnixNano())
	return &Pinger{
		id:     rand.Intn(0xffff),
		seq:    rand.Intn(0xffff),
		addrs:  make(map[string]*net.IPAddr),
		Size:   TimeSliceLength,
		MaxRTT: time.Second,
		OnRecv: nil,
		OnIdle: nil,
		Debug:  false,
	}
}

// AddIP adds an IP address to Pinger. ipaddr arg should be a string like
// "192.0.2.1".
func (p *Pinger) AddIP(ipaddr string) error {

	addr := net.ParseIP(ipaddr)
	if addr == nil {
		return fmt.Errorf("%s is not a valid textual representation of an IP address", ipaddr)
	}
	p.mu.Lock()
	debug := p.Debug
	p.addrs[addr.String()] = &net.IPAddr{IP: addr}
	p.mu.Unlock()
	if debug {
		log.Println("AddIP(): " + ipaddr)
	}
	return nil
}

// AddIPAddr adds an IP address to Pinger. ip arg should be a net.IPAddr
// pointer.
func (p *Pinger) AddIPAddr(ip *net.IPAddr) {

	p.mu.Lock()
	debug := p.Debug
	p.addrs[ip.String()] = ip
	p.mu.Unlock()

	if debug {
		log.Println("AddIPAddr: " + ip.String())
	}
}

// AddHandler adds event handler to Pinger. event arg should be "receive" or
// "idle" string.
//
// **CAUTION** This function is deprecated. Please use OnRecv and OnIdle field
// of Pinger struct to set following handlers.
//
// "receive" handler should be
//
//	func(addr *net.IPAddr, rtt time.Duration)
//
// type function. The handler is called with a response packet's source address
// and its elapsed time when Pinger receives a response packet.
//
// "idle" handler should be
//
//	func()
//
// type function. The handler is called when MaxRTT time passed. For more
// detail, please see Run() and RunLoop().
func (p *Pinger) AddHandler(event string, handler interface{}) error {
	switch event {
	case "receive":
		if hdl, ok := handler.(func(*net.IPAddr, time.Duration)); ok {
			p.mu.Lock()
			p.OnRecv = hdl
			p.mu.Unlock()
			return nil
		}
		return errors.New("receive event handler should be `func(*net.IPAddr, time.Duration)`")
	case "idle":
		if hdl, ok := handler.(func()); ok {
			p.mu.Lock()
			p.OnIdle = hdl
			p.mu.Unlock()
			return nil
		}
		return errors.New("idle event handler should be `func()`")
	}
	return errors.New("No such event: " + event)
}

// Run invokes a single send/receive procedure. It sends packets to all hosts
// which have already been added by AddIP() etc. and wait those responses. When
// it receives a response, it calls "receive" handler registered by AddHander().
// After MaxRTT seconds, it calls "idle" handler and returns to caller with
// an error value. It means it blocks until MaxRTT seconds passed. For the
// purpose of sending/receiving packets over and over, use RunLoop().
func (p *Pinger) Run() error {
	p.mu.Lock()
	p.ctx = newContext()
	p.mu.Unlock()
	p.run(true, p.Debug)
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ctx.err
}

// RunLoop invokes send/receive procedure repeatedly. It sends packets to all
// hosts which have already been added by AddIP() etc. and wait those responses.
// When it receives a response, it calls "receive" handler registered by
// AddHander(). After MaxRTT seconds, it calls "idle" handler, resend packets
// and wait those response. MaxRTT works as an interval time.
//
// This is a non-blocking method so immediately returns. If you want to monitor
// and stop sending packets, use Done() and Stop() methods. For example,
//
//	p.RunLoop()
//	ticker := time.NewTicker(time.Millisecond * 250)
//	select {
//	case <-p.Done():
//		if err := p.Err(); err != nil {
//			log.Fatalf("Ping failed: %v", err)
//		}
//	case <-ticker.C:
//		break
//	}
//	ticker.Stop()
//	p.Stop()
//
// For more details, please see "cmd/ping/ping.go".
func (p *Pinger) RunLoop() {
	p.mu.Lock()
	p.ctx = newContext()
	p.mu.Unlock()
	go p.run(false, p.Debug)
}

// Done returns a channel that is closed when RunLoop() is stopped by an error
// or Stop(). It must be called after RunLoop() call. If not, it causes panic.
func (p *Pinger) Done() <-chan bool {
	return p.ctx.done
}

// Stop stops RunLoop(). It must be called after RunLoop(). If not, it causes
// panic.
func (p *Pinger) Stop() {
	p.mu.Lock()
	if p.Debug {
		log.Println("Stop(): close(p.ctx.stop)")
	}
	close(p.ctx.stop)
	if p.Debug {
		log.Println("Stop(): <-p.ctx.done")
	}
	p.mu.Unlock()
	<-p.ctx.done
}

// Err returns an error that is set by RunLoop(). It must be called after
// RunLoop(). If not, it causes panic.
func (p *Pinger) Err() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ctx.err
}

func (p *Pinger) listen(netProto string) *net.IPConn {
	conn, err := net.ListenIP(netProto, nil)
	if err != nil {
		p.mu.Lock()
		p.ctx.err = err
		debug := p.Debug
		p.mu.Unlock()
		if debug {
			log.Println("Run(): close(p.ctx.done)")
		}
		close(p.ctx.done)
		return nil
	}
	return conn
}

func (p *Pinger) run(once bool, debug bool) {
	if debug {
		log.Println("Run(): Start")
	}
	var conn, conn6 *net.IPConn
	if conn = p.listen("ip4:icmp"); conn == nil {
		return
	}
	defer conn.Close()

	if conn6 = p.listen("ip6:ipv6-icmp"); conn6 == nil {
		return
	}
	defer conn6.Close()

	recvCtx := newContext()
	wg := new(sync.WaitGroup)

	if debug {
		log.Println("Run(): call sendICMP()")
	}
	var err error
	err = p.sendICMP(conn, conn6, debug)

	p.mu.Lock()
	idle_handler := p.OnIdle
	recv_handler := p.OnRecv
	p.mu.Unlock()

	if debug {
		log.Println("Run(): call recvICMP()")
	}
	if conn != nil {
		wg.Add(1)
		go p.recvICMP(conn, recv_handler, recvCtx, wg, debug)
	}
	if conn6 != nil {
		wg.Add(1)
		go p.recvICMP(conn6, recv_handler, recvCtx, wg, debug)
	}

	p.mu.Lock()
	ticker := time.NewTicker(p.MaxRTT)
	p.mu.Unlock()
mainloop:
	for {
		select {
		case <-p.ctx.stop:
			if debug {
				log.Println("Run(): <-p.ctx.stop")
			}
			break mainloop
		case <-recvCtx.done:
			if debug {
				log.Println("Run(): <-recvCtx.done")
			}
			p.mu.Lock()
			err = recvCtx.err
			p.mu.Unlock()
			break mainloop
		case <-ticker.C:
			if idle_handler != nil {
				idle_handler()
			}
			if once || err != nil {
				break mainloop
			}
			if debug {
				log.Println("Run(): call sendICMP()")
			}
			err = p.sendICMP(conn, conn6, debug)
		}
	}

	ticker.Stop()
	if debug {
		log.Println("Run(): close(recvCtx.stop)")
	}
	close(recvCtx.stop)
	if debug {
		log.Println("Run(): wait recvICMP()")
	}
	wg.Wait()

	p.mu.Lock()
	p.ctx.err = err
	p.mu.Unlock()

	if debug {
		log.Println("Run(): close(p.ctx.done)")
	}
	close(p.ctx.done)
	if debug {
		log.Println("Run(): End")
	}
}

func (p *Pinger) sendICMP(conn, conn6 *net.IPConn, debug bool) error {
	id := rand.Intn(0xffff)
	seq := rand.Intn(0xffff)

	local_addrs := make(map[string]*net.IPAddr)

	if debug {
		log.Println("sendICMP(): Start")
	}
	p.mu.Lock()
	p.id = id
	p.seq = seq
	for k, v := range p.addrs {
		local_addrs[k] = v
	}
	p.mu.Unlock()
	ticker := time.NewTicker(time.Millisecond * 100)
	wg := new(sync.WaitGroup)
	for _, addr := range local_addrs {
		var typ int
		var cn *net.IPConn
		if isIPv4(addr.IP) {
			typ = icmpv4EchoRequest
			cn = conn
		} else if isIPv6(addr.IP) {
			typ = icmpv6EchoRequest
			cn = conn6
		} else {
			continue
		}
		if cn == nil {
			continue
		}

		if debug {
			log.Println("sendICMP(): Invoke goroutine")
		}
		wg.Add(1)
		// rate limit packet sending to avoid overwhelming the network
		<-ticker.C
		go func(conn *net.IPConn, ra *net.IPAddr, pkt_typ int, debug bool) {
			t := timeToBytes(time.Now())

			if p.Size-TimeSliceLength != 0 {
				t = append(t, byteSliceOfSize(p.Size-TimeSliceLength)...)
			}

			bytes, err := (&icmpMessage{
				Type: pkt_typ, Code: 0,
				Body: &icmpEcho{
					ID: id, Seq: seq,
					Data: t,
				},
			}).Marshal()

			if err != nil {
				log.Println(err)
				return
			}
			for {
				if _, err := conn.WriteToIP(bytes, ra); err != nil {
					if neterr, ok := err.(*net.OpError); ok {
						if neterr.Err == syscall.ENOBUFS {
							log.Println("ENOBUFS")
							continue
						}
					}
				}
				break
			}
			if debug {
				log.Println("sendICMP(): WriteMsgIP End")
			}
			wg.Done()
		}(cn, addr, typ, debug)
	}
	wg.Wait()
	ticker.Stop()
	if debug {
		log.Println("sendICMP(): End")
	}
	return nil
}

func (p *Pinger) recvICMP(conn *net.IPConn, recv_handler func(*net.IPAddr, time.Duration), ctx *context, wg *sync.WaitGroup, debug bool) {
	if debug {
		log.Println("recvICMP(): Start")
	}
	for {
		select {
		case <-ctx.stop:
			if debug {
				log.Println("recvICMP(): <-ctx.stop")
			}
			wg.Done()
			if debug {
				log.Println("recvICMP(): wg.Done()")
			}
			return
		default:
		}

		bytes := make([]byte, 512)
		conn.SetReadDeadline(time.Now().Add(time.Millisecond * 100))
		if debug {
			log.Println("recvICMP(): ReadMsgIP Start")
		}
		n, ra, err := conn.ReadFromIP(bytes)
		if debug {
			log.Println("recvICMP(): ReadMsgIP End")
		}
		if err != nil {
			if neterr, ok := err.(*net.OpError); ok {
				if neterr.Timeout() {
					continue
				} else {
					log.Println("recv Error", err)
					p.mu.Lock()
					ctx.err = err
					p.mu.Unlock()
					close(ctx.done)
					wg.Done()
					return
				}
			}
		}
		if debug {
			log.Println("recvICMP(): recv packet")
		}
		var m *icmpMessage
		if m, err = parseICMPMessage(bytes[:n]); err != nil {
			continue
		}
		if m.Type != icmpv4EchoReply && m.Type != icmpv6EchoReply {
			continue
		}
		if debug {
			log.Println("recvICMP(): recv reply")
		}
		go p.procRecv(&packet{msg: m, addr: ra}, recv_handler)
	}
}

func (p *Pinger) procRecv(recv *packet, recv_handler func(*net.IPAddr, time.Duration)) {
	var rtt time.Duration
	switch pkt := recv.msg.Body.(type) {
	case *icmpEcho:
		time_from_packet := bytesToTime(pkt.Data[:TimeSliceLength])
		rtt = time.Since(time_from_packet)
	default:
		return
	}

	if recv_handler != nil {
		recv_handler(recv.addr, rtt)
	}
}
