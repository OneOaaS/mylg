// Copyright 2016 Mehrdad Arshad Rad <arshad.rad@gmail.com>. All rights reserved.
// Use of this source code is governed by a MIT license that can
// be found in the LICENSE file.

package icmp

import (
	"fmt"
	"math/rand"
	"net"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	ui "github.com/gizak/termui"

	"github.com/mehrdadrad/mylg/cli"
	"github.com/mehrdadrad/mylg/ripe"
)

// MHopResp represents multi hop's responses
type MHopResp []HopResp

// NewTrace creates new trace object
func NewTrace(args string, cfg cli.Config) (*Trace, error) {
	var (
		family int
		proto  int
		ip     net.IP
	)
	target, flag := cli.Flag(args)
	forceIPv4 := cli.SetFlag(flag, "4", false).(bool)
	forceIPv6 := cli.SetFlag(flag, "6", false).(bool)
	// show help
	if _, ok := flag["help"]; ok || len(target) < 3 {
		helpTrace()
		return nil, nil
	}
	ips, err := net.LookupIP(target)
	if err != nil {
		return nil, err
	}
	for _, IP := range ips {
		if IsIPv4(IP) && !forceIPv6 {
			ip = IP
			break
		} else if IsIPv6(IP) && !forceIPv4 {
			ip = IP
			break
		}
	}

	if ip == nil {
		return nil, fmt.Errorf("there is not A or AAAA record")
	}

	if IsIPv4(ip) {
		family = syscall.AF_INET
		proto = syscall.IPPROTO_ICMP
	} else {
		family = syscall.AF_INET6
		proto = syscall.IPPROTO_ICMPV6
	}

	return &Trace{
		host:     target,
		ips:      ips,
		ip:       ip,
		family:   family,
		proto:    proto,
		pSize:    52,
		wait:     cli.SetFlag(flag, "w", cfg.Trace.Wait).(string),
		icmp:     cli.SetFlag(flag, "I", false).(bool),
		resolve:  cli.SetFlag(flag, "n", true).(bool),
		ripe:     cli.SetFlag(flag, "nr", true).(bool),
		realTime: cli.SetFlag(flag, "r", false).(bool),
		maxTTL:   cli.SetFlag(flag, "m", 30).(int),
	}, nil
}

func (h MHopResp) Len() int           { return len(h) }
func (h MHopResp) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h MHopResp) Less(i, j int) bool { return len(h[i].ip) > len(h[j].ip) }

// SetTTL set the IP packat time to live
func (i *Trace) SetTTL(ttl int) {
	i.ttl = ttl
}

// Send tries to send ICMP packet
func (i *Trace) Send(port int) (int, int, error) {
	rand.Seed(time.Now().UTC().UnixNano())
	var (
		seq    = rand.Intn(0xff) //TODO: sequence
		id     = os.Getpid() & 0xffff
		sotype int
		proto  int
		err    error
	)

	if i.icmp && i.ip.To4 != nil {
		sotype = syscall.SOCK_RAW
		proto = syscall.IPPROTO_ICMP
	} else if i.icmp && i.ip.To16 != nil {
		sotype = syscall.SOCK_RAW
		proto = syscall.IPPROTO_ICMPV6
	} else {
		sotype = syscall.SOCK_DGRAM
		proto = syscall.IPPROTO_UDP
	}

	fd, err := syscall.Socket(i.family, sotype, proto)
	if err != nil {
		return id, seq, err
	}
	defer syscall.Close(fd)

	// Set options
	if IsIPv4(i.ip) {
		var b [4]byte
		copy(b[:], i.ip.To4())
		addr := syscall.SockaddrInet4{
			Port: port,
			Addr: b,
		}

		m, err := icmpV4Message(id, seq)
		if err != nil {
			return id, seq, err
		}

		setIPv4TTL(fd, i.ttl)

		if err := syscall.Sendto(fd, m, 0, &addr); err != nil {
			return id, seq, err
		}
	} else {
		var b [16]byte
		copy(b[:], i.ip.To16())
		addr := syscall.SockaddrInet6{
			Port:   port,
			ZoneId: 0,
			Addr:   b,
		}

		m, err := icmpV6Message(id, seq)
		if err != nil {
			return id, seq, err
		}

		setIPv6HopLimit(fd, i.ttl)

		if err := syscall.Sendto(fd, m, 0, &addr); err != nil {
			return id, seq, err
		}
	}
	return id, seq, nil
}

// SetReadDeadLine sets rx timeout
func (i *Trace) SetReadDeadLine() error {
	timeout, err := time.ParseDuration(i.wait)
	if err != nil {
		return err
	}
	tv := syscall.NsecToTimeval(timeout.Nanoseconds())
	return syscall.SetsockoptTimeval(i.fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv)
}

// SetWriteDeadLine sets tx timeout
func (i *Trace) SetWriteDeadLine() error {
	tv := syscall.NsecToTimeval(1e6 * DefaultTXTimeout)
	return syscall.SetsockoptTimeval(i.fd, syscall.SOL_SOCKET, syscall.SO_SNDTIMEO, &tv)
}

// SetDeadLine sets tx/rx timeout
func (i *Trace) SetDeadLine() error {
	err := i.SetReadDeadLine()
	if err != nil {
		return err
	}
	err = i.SetWriteDeadLine()
	if err != nil {
		return err
	}
	return nil
}

// Bind starts to listen for ICMP reply
func (i *Trace) Bind() error {
	var err error

	i.fd, err = syscall.Socket(i.family, syscall.SOCK_RAW, i.proto)
	if err != nil {
		return os.NewSyscallError("bindsocket", err)
	}

	err = i.SetDeadLine()
	if err != nil {
		println(err.Error())
	}

	if i.family == syscall.AF_INET {
		addr := syscall.SockaddrInet4{
			Port: 0,
			Addr: [4]byte{},
		}

		if err := syscall.Bind(i.fd, &addr); err != nil {
			return os.NewSyscallError("bindv4", err)
		}
	} else {
		addr := syscall.SockaddrInet6{
			Port:   0,
			ZoneId: 0,
			Addr:   [16]byte{},
		}

		if err := syscall.Bind(i.fd, &addr); err != nil {
			return os.NewSyscallError("bindv6", err)
		}

	}
	return nil
}

// Recv gets the replied icmp packet
func (i *Trace) Recv(id, seq int) (ICMPResp, error) {
	var (
		b    = make([]byte, 512)
		ts   = time.Now()
		resp ICMPResp
		wID  bool
		wSeq bool
		wDst bool
	)

	for {
		n, from, err := syscall.Recvfrom(i.fd, b, 0)

		if err != nil {
			du, _ := time.ParseDuration(i.wait)
			if err == syscall.EAGAIN && time.Since(ts) < du {
				continue
			}
			return resp, err
		}

		b = b[:n]

		if len(i.ip.To4()) == net.IPv4len {
			resp = icmpV4RespParser(b)
			wID = resp.typ == IPv4ICMPTypeEchoReply && id != resp.id
			wSeq = seq != resp.seq
			wDst = resp.ip.dst.String() != i.ip.String()
		} else {
			resp = icmpV6RespParser(b)
			resp.src = net.IP(from.(*syscall.SockaddrInet6).Addr[:])
			wID = resp.typ == IPv6ICMPTypeEchoReply && id != resp.id
			wSeq = seq != resp.seq
			wDst = resp.ip.dst.String() != i.ip.String()
		}

		if (i.icmp && wSeq) || wDst || wID {
			du, _ := time.ParseDuration(i.wait)
			if time.Since(ts) < du {
				continue
			}
			return resp, fmt.Errorf("wrong response")
		}

		break
	}
	return resp, nil
}

// NextHop pings the specific hop by set TTL
func (i *Trace) NextHop(hop int) HopResp {
	rand.Seed(time.Now().UTC().UnixNano())
	var (
		r    = HopResp{num: hop}
		port = 33434
		name []string
	)
	i.SetTTL(hop)
	begin := time.Now()

	id, seq, err := i.Send(port)
	if err != nil {
		return HopResp{num: hop, err: err}
	}

	resp, err := i.Recv(id, seq)
	if err != nil {
		r = HopResp{hop, "", "", 0, false, nil, Whois{}}
		return r
	}

	elapsed := time.Since(begin)

	if i.resolve {
		name, _ = lookupAddr(resp.src)
	}
	if len(name) > 0 {
		r = HopResp{hop, name[0], resp.src.String(), elapsed.Seconds() * 1e3, false, nil, Whois{}}
	} else {
		r = HopResp{hop, "", resp.src.String(), elapsed.Seconds() * 1e3, false, nil, Whois{}}
	}
	// reached to the target
	for _, h := range i.ips {
		if resp.src.String() == h.String() {
			r.last = true
			break
		}
	}
	return r
}

// Run provides trace based on the other methods
func (i *Trace) Run(retry int) chan []HopResp {
	var (
		c = make(chan []HopResp, 1)
		r []HopResp
	)
	i.Bind()
	go func() {
	LOOP:
		for h := 1; h <= i.maxTTL; h++ {
			for n := 0; n < retry; n++ {
				hop := i.NextHop(h)
				r = append(r, hop)
				if hop.err != nil {
					break
				}
			}
			if i.ripe {
				i.addWhois(r[:])
			}
			c <- r
			for _, R := range r {
				if R.last || R.err != nil {
					break LOOP
				}
			}
			r = r[:0]
		}
		close(c)
		syscall.Close(i.fd)
	}()
	return c
}

// MRun provides trace all hops in loop
func (i *Trace) MRun() (chan HopResp, error) {
	var (
		c      = make(chan HopResp, 1)
		ASN    = make(map[string]Whois, 100)
		maxTTL = i.maxTTL
	)

	if err := i.Bind(); err != nil {
		return c, err
	}

	go func() {
		defer func() {
			if err := recover(); err != nil {
				syscall.Close(i.fd)
			}
		}()
		for {
			for h := 1; h <= maxTTL; h++ {
				hop := i.NextHop(h)
				if w, ok := ASN[hop.ip]; ok {
					hop.whois = w
				} else if hop.ip != "" {
					go func(ASN map[string]Whois) {
						w, _ := whois(hop.ip)
						ASN[hop.ip] = w
					}(ASN)
				}

				c <- hop

				if hop.last && maxTTL == i.maxTTL {
					maxTTL = h
				}
			}
			time.Sleep(1 * time.Second)
		}
	}()
	return c, nil
}

// TermUI prints out trace loop by termui
func (i *Trace) TermUI() error {
	ui.DefaultEvtStream = ui.NewEvtStream()
	if err := ui.Init(); err != nil {
		return err
	}
	defer ui.Close()

	var (
		done    = make(chan struct{})
		routers = make([]map[string]Stats, 65)

		// columns
		hops = ui.NewList()
		asn  = ui.NewList()
		rtt  = ui.NewList()
		snt  = ui.NewList()
		pkl  = ui.NewList()

		stats = make([]Stats, 65)
		lists = []*ui.List{hops, asn, rtt, snt, pkl}

		rChanged bool
	)

	resp, err := i.MRun()
	if err != nil {
		return err
	}

	for _, l := range lists {
		l.Items = make([]string, 65)
		l.X = 0
		l.Y = 0
		l.Height = 35
		l.Border = false
	}

	for i := 1; i < 65; i++ {
		routers[i] = make(map[string]Stats, 30)
	}

	// lince chart
	lc := ui.NewLineChart()
	lc.BorderLabel = fmt.Sprintf("RTT: %s", i.host)
	lc.Height = 18
	lc.X = 0
	lc.Y = 0
	lc.Mode = "dot"
	lc.AxesColor = ui.ColorWhite
	lc.LineColor = ui.ColorGreen | ui.AttrBold

	// title
	hops.Items[0] = fmt.Sprintf("[%-50s](fg-bold)", "Host")
	asn.Items[0] = fmt.Sprintf("[ %-6s %-6s](fg-bold)", "ASN", "Holder")
	rtt.Items[0] = fmt.Sprintf("[%-6s %-6s %-6s %-6s](fg-bold)", "Last", "Avg", "Best", "Wrst")
	snt.Items[0] = "[Sent](fg-bold)"
	pkl.Items[0] = "[Loss%](fg-bold)"

	// header
	header := ui.NewPar(fmt.Sprintf("myLG - traceroute to %s (%s), %d hops max", i.host, i.ip, i.maxTTL))
	header.Height = 1
	header.Width = ui.TermWidth()
	header.Y = 1
	header.TextBgColor = ui.ColorBlue
	header.Border = false

	// menu
	menu := ui.NewPar("Press [q] to quit, [r] to reset statistics, [1,2] to change display mode")
	menu.Height = 2
	menu.Width = 20
	menu.Y = 1
	menu.Border = false

	// screens1 - trace statistics
	screen1 := []*ui.Row{
		ui.NewRow(
			ui.NewCol(12, 0, header),
		),
		ui.NewRow(
			ui.NewCol(12, 0, menu),
		),
		ui.NewRow(
			ui.NewCol(5, 0, hops),
			ui.NewCol(2, 0, asn),
			ui.NewCol(1, 0, pkl),
			ui.NewCol(1, 0, snt),
			ui.NewCol(3, 0, rtt),
		),
	}
	// screen2 - trace line chart
	screen2 := []*ui.Row{
		ui.NewRow(
			ui.NewCol(12, 0, header),
		),
		ui.NewRow(
			ui.NewCol(12, 0, menu),
		),
		ui.NewRow(
			ui.NewCol(12, 0, lc),
		),
	}

	// init layout
	ui.Body.AddRows(screen1...)
	ui.Body.Align()
	ui.Render(ui.Body)

	ui.Handle("/sys/wnd/resize", func(e ui.Event) {
		ui.Body.Width = ui.TermWidth()
		ui.Body.Align()
		ui.Render(ui.Body)
	})

	ui.Handle("/sys/kbd/q", func(ui.Event) {
		done <- struct{}{}
		ui.StopLoop()
	})

	// reset statistics and display
	ui.Handle("/sys/kbd/r", func(ui.Event) {
		for i := 1; i < 65; i++ {
			for _, l := range lists {
				l.Items[i] = ""
			}
			stats[i].count = 0
			stats[i].avg = 0
			stats[i].min = 0
			stats[i].max = 0
			stats[i].pkl = 0
		}
		lc.Data = lc.Data[:0]
		lc.DataLabels = lc.DataLabels[:0]
	})

	// change display mode to one
	ui.Handle("/sys/kbd/1", func(e ui.Event) {
		ui.Body.Rows = ui.Body.Rows[:0]
		ui.Body.AddRows(screen1...)
		ui.Body.Align()
		ui.Render(ui.Body)
	})

	// change display mode to two
	ui.Handle("/sys/kbd/2", func(e ui.Event) {
		ui.Body.Rows = ui.Body.Rows[:0]
		ui.Body.AddRows(screen2...)
		ui.Body.Align()
		ui.Render(ui.Body)
	})

	go func() {
		var (
			hop, as, holder string
		)
	LOOP:
		for {
			select {
			case <-done:
				break LOOP
			case r, ok := <-resp:
				if !ok {
					break LOOP
				}

				if r.hop != "" {
					hop = r.hop
				} else {
					hop = r.ip
				}

				if r.whois.asn > 0 {
					as = fmt.Sprintf("%.0f", r.whois.asn)
					holder = strings.Fields(r.whois.holder)[0]
				} else {
					as = ""
					holder = ""
				}

				// statistics
				stats[r.num].count++
				snt.Items[r.num] = fmt.Sprintf("%d", stats[r.num].count)

				router := routers[r.num][hop]
				router.count++

				if r.elapsed != 0 {

					// hop level statistics
					calcStatistics(&stats[r.num], r.elapsed)
					// router level statistics
					calcStatistics(&router, r.elapsed)
					// detect router changes
					rChanged = routerChange(hop, hops.Items[r.num])

					hops.Items[r.num] = fmt.Sprintf("[%-2d] %-45s", r.num, hop)
					asn.Items[r.num] = fmt.Sprintf("%-6s %s", as, holder)
					rtt.Items[r.num] = fmt.Sprintf("%-6.2f\t%-6.2f\t%-6.2f\t%-6.2f", r.elapsed, stats[r.num].avg, stats[r.num].min, stats[r.num].max)

					if rChanged {
						hops.Items[r.num] = termUICColor(hops.Items[r.num], "fg-bold")
					}

					lcShift(r, lc, ui.TermWidth())

				} else if r.elapsed == 0 && hops.Items[r.num] == "" {

					hops.Items[r.num] = fmt.Sprintf("[%-2d] %-40s", r.num, "???")
					stats[r.num].pkl++
					router.pkl++

				} else if !strings.Contains(hops.Items[r.num], "???") {

					hops.Items[r.num] = termUICColor(hops.Items[r.num], "fg-red")
					rtt.Items[r.num] = fmt.Sprintf("%-6.2s\t%-6.2f\t%-6.2f\t%-6.2f", "?", stats[r.num].avg, stats[r.num].min, stats[r.num].max)
					stats[r.num].pkl++
					router.pkl++

				} else {

					stats[r.num].pkl++
					router.pkl++

				}

				routers[r.num][hop] = router

				pkl.Items[r.num] = fmt.Sprintf("%.1f", float64(stats[r.num].pkl)*100/float64(stats[r.num].count))
				ui.Render(ui.Body)
				// clean up in case of packet loss on the last hop at first try
				if r.last {
					for i := r.num + 1; i < 65; i++ {
						hops.Items[i] = ""
					}
				}
			}
		}
		close(resp)
	}()

	ui.Loop()
	return nil
}

// routerChange detects if the router changed
// to another one
func routerChange(router, b string) bool {
	if b != "" {
		bRouter := strings.Fields(b)
		if len(bRouter) < 2 {
			return false
		}
		hop := strings.Split(b, "] ")
		if len(hop) < 2 {
			return false
		}
		if strings.Fields(hop[1])[0] != router {
			return true
		}
	}
	return false
}

// lcShift shifs line chart once it filled out
func lcShift(r HopResp, lc *ui.LineChart, width int) {
	if r.last {
		t := time.Now()
		lc.Data = append(lc.Data, r.elapsed)
		lc.DataLabels = append(lc.DataLabels, t.Format("04:05"))
		if len(lc.Data) > ui.TermWidth()-10 {
			lc.Data = lc.Data[1:]
			lc.DataLabels = lc.DataLabels[1:]
		}
	}
}

func termUICColor(m, color string) string {
	if !strings.Contains(m, color) {
		m = fmt.Sprintf("[%s](%s)", m, color)
	}
	return m
}

// Print prints out trace result in normal or terminal mode
func (i *Trace) Print() {
	if i.realTime {
		if err := i.TermUI(); err != nil {
			fmt.Println(err.Error())
		}
	} else {
		i.PrintPretty()
	}
}

// PrintPretty prints out trace result
func (i *Trace) PrintPretty() {
	var (
		counter int
		sigCh   = make(chan os.Signal, 1)
		resp    = i.Run(3)
	)

	signal.Notify(sigCh, os.Interrupt)
	defer signal.Stop(sigCh)

	// header
	fmt.Printf("trace route to %s (%s), %d hops max\n", i.host, i.ip, i.maxTTL)
LOOP:
	for {
		select {
		case r, ok := <-resp:
			if !ok {
				break LOOP
			}
			for _, R := range r {
				if R.err != nil {
					println(R.err.Error())
					break LOOP
				}
			}
			counter++
			sort.Sort(MHopResp(r))
			// there is not any load balancing and there is at least a timeout
			if r[0].ip != r[1].ip && (r[1].elapsed == 0 || r[2].elapsed == 0) {
				fmt.Printf("%-2d %s", counter, fmtHops(r, 1))
				continue
			}
			// there is not any load balancing and there is at least a timeout
			if r[1].ip != r[2].ip && (r[0].elapsed == 0 || r[1].elapsed == 0) {
				fmt.Printf("%-2d %s", counter, fmtHops(r, 1))
				continue
			}
			// there is not any load balancing and there is at least a timeout
			if r[0].ip == r[1].ip && r[0].elapsed != 0 && r[2].elapsed == 0 {
				fmt.Printf("%-2d %s %s", counter, fmtHops(r[0:2], 0), fmtHops(r[2:3], 1))
				continue
			}

			// load balance between three routes
			if r[0].ip != r[1].ip && r[1].ip != r[2].ip {
				fmt.Printf("%-2d %s   %s   %s", counter, fmtHops(r[0:1], 1), fmtHops(r[1:2], 1), fmtHops(r[2:3], 1))
				continue
			}
			// load balance between two routes
			if r[0].ip == r[1].ip && r[1].ip != r[2].ip {
				fmt.Printf("%-2d %s   %s", counter, fmtHops(r[0:2], 1), fmtHops(r[2:3], 1))
				continue
			}
			// load balance between two routes
			if r[0].ip != r[1].ip && r[1].ip == r[2].ip {
				fmt.Printf("%-2d %s   %s", counter, fmtHops(r[0:1], 1), fmtHops(r[1:3], 1))
				continue
			}
			// there is not any load balancing
			if r[0].ip == r[1].ip && r[1].ip == r[2].ip {
				fmt.Printf("%-2d %s", counter, fmtHops(r, 1))
			}
			//fmt.Printf("%#v\n", r)
		case <-sigCh:
			break LOOP
		}
	}
}

func fmtHops(m []HopResp, newLine int) string {
	var (
		timeout = false
		msg     string
	)
	for _, r := range m {
		if (msg == "" || timeout) && r.hop != "" {
			if r.whois.asn != 0 {
				msg += fmt.Sprintf("%s (%s) [ASN %.0f/%s] ", r.hop, r.ip, r.whois.asn, strings.Fields(r.whois.holder)[0])
			} else {
				msg += fmt.Sprintf("%s (%s) ", r.hop, r.ip)
			}
		}
		if (msg == "" || timeout) && r.hop == "" && r.elapsed != 0 {
			if r.whois.asn != 0 {
				msg += fmt.Sprintf("%s [ASN %.0f/%s] ", r.ip, r.whois.asn, strings.Fields(r.whois.holder)[0])
			} else {
				msg += fmt.Sprintf("%s ", r.ip)
			}
		}
		if r.elapsed != 0 {
			msg += fmt.Sprintf("%.3f ms ", r.elapsed)
			timeout = false
		} else {
			msg += "* "
			timeout = true
		}
	}
	if newLine == 1 {
		msg += "\n"
	}
	return msg
}

// addWhois adds whois info to response if available
func (i *Trace) addWhois(R []HopResp) {
	var (
		ips = make(map[string]Whois, 3)
		w   Whois
		err error
	)

	for _, r := range R {
		ips[r.ip] = Whois{}
	}

	for ip := range ips {
		if ip == "" {
			continue
		}

		w, err = whois(ip)

		if err != nil {
			continue
		}

		ips[ip] = w
	}

	for i := range R {
		R[i].whois = ips[R[i].ip]
	}
}

// whois returns prefix whois info from RIPE
func whois(ip string) (Whois, error) {
	var resp Whois

	_, net, err := net.ParseCIDR(ip + "/24")
	if err != nil {
		ip = net.String()
	}

	r := new(ripe.Prefix)
	r.Set(ip)
	r.GetData()
	data, ok := r.Data["data"].(map[string]interface{})
	if !ok {
		return Whois{}, fmt.Errorf("data not available")
	}
	asns := data["asns"].([]interface{})
	for _, h := range asns {
		resp.holder = h.(map[string]interface{})["holder"].(string)
		resp.asn = h.(map[string]interface{})["asn"].(float64)
	}
	return resp, nil
}

func min(a, b float64) float64 {
	if a == 0 {
		return b
	}
	if a < b {
		return a
	}
	return b
}
func avg(a, b float64) float64 {
	if a != 0 {
		return (a + b) / 2
	}
	return b
}
func max(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
func lookupAddr(ip net.IP) ([]string, error) {
	var (
		c = make(chan []string, 1)
		r []string
	)

	go func() {
		n, _ := net.LookupAddr(ip.String())
		c <- n
	}()
	select {
	case r = <-c:
		return r, nil
	case <-time.After(1 * time.Second):
		return r, fmt.Errorf("lookup.addr timeout")
	}
}

func calcStatistics(s *Stats, elapsed float64) {
	s.min = min(s.min, elapsed)
	s.avg = avg(s.avg, elapsed)
	s.max = max(s.max, elapsed)
}

func helpTrace() {
	fmt.Println(`
    usage:
          trace IP address / domain name [options]
    options:
          -r             Real-time response time at each point along the way
          -n             Do not try to map IP addresses to host names
          -nr            Do not try to map IP addresses to ASN,Holder (RIPE NCC)
          -m MAX_TTL     Specifies the maximum number of hops
          -4             Forces the trace command to use IPv4 (target should be hostname)
          -6             Forces the trace command to use IPv6 (target should be hostname)
    Example:
          trace 8.8.8.8
          trace freebsd.org -r
	`)

}
