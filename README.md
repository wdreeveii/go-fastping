go-fastping
===========

go-fastping is a Go language's ICMP ping library inspired by AnyEvent::FastPing
Perl module to send ICMP ECHO REQUEST packets quickly. Original Perl module is
available at
http://search.cpan.org/~mlehmann/AnyEvent-FastPing-2.01/

It hasn't been fully implemented original functions yet. There are interface changes coming so you must be ready to deal with that.

[![GoDoc](https://godoc.org/github.com/wdreeveii/go-fastping?status.svg)](https://godoc.org/github.com/wdreeveii/go-fastping)

## Installation

Install and update this go package with `go get -u github.com/wdreeveii/go-fastping`

## Examples

Import this package and write

```go
p := fastping.NewPinger()
ra, err := net.ResolveIPAddr("ip4:icmp", os.Args[1])
if err != nil {
	fmt.Println(err)
	os.Exit(1)
}
p.AddIPAddr(ra)
p.OnRecv = func(addr *net.IPAddr, rtt time.Duration) {
	fmt.Printf("IP Addr: %s receive, RTT: %v\n", addr.String(), rtt)
}
p.OnIdle = func() {
	fmt.Println("finish")
}
err = p.Run()
if err != nil {
	fmt.Println(err)
}
```

It sends an ICMP packet and wait a response. If it receives a response, it
calls "receive" callback. After that, MaxRTT time passed, it calls "idle"
callback. For more detail, refer [godoc][godoc] and If you need more example,
please see "cmd/ping/ping.go".

## License
go-fastping is under MIT License. See the [LICENSE][license] file for details.

[godoc]: http://godoc.org/github.com/wdreeveii/go-fastping
[license]: https://github.com/wdreeveii/go-fastping/blob/master/LICENSE
